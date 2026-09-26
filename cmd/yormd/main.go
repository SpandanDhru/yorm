// Command yormd is the Yorm server.
//
//	yormd                 run the server (see internal/config for env vars)
//	yormd mint-token ...  print a join token for an existing session, for debugging
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/config"
	"github.com/SpandanDhru/yorm/internal/db"
	"github.com/SpandanDhru/yorm/internal/httpapi"
	"github.com/SpandanDhru/yorm/internal/metrics"
	"github.com/SpandanDhru/yorm/internal/session"
	"github.com/SpandanDhru/yorm/internal/store"
	"github.com/SpandanDhru/yorm/internal/ws"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mint-token" {
		if err := mintToken(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "mint-token:", err)
			os.Exit(1)
		}
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("yormd exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	signer, err := auth.NewSigner(cfg.TokenSecret)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.UploadDir, 0o750); err != nil {
		return fmt.Errorf("upload dir: %w", err)
	}
	st := store.New(pool)
	if cfg.GroupCommit > 0 {
		st.GroupCommit(cfg.GroupCommit, 64)
		defer st.Close() // after the sessions stop, before the pool closes
	}
	sessions := session.NewManager(st, log, session.DefaultOptions())
	opts := ws.DefaultOptions()
	opts.OriginPatterns = cfg.AllowedOrigins
	wsSrv := ws.NewServer(signer, sessions, log, opts)
	metrics.Gauge("yorm_active_sessions", "Sessions with a running actor.", func() float64 { return float64(sessions.Active()) })
	metrics.Gauge("yorm_active_connections", "Open WebSocket connections.", func() float64 { return float64(wsSrv.ConnCount()) })

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Log: log, WS: wsSrv, Health: pool.Ping, Signer: signer, Store: st, Sessions: sessions,
			UploadDir: cfg.UploadDir, WebDir: cfg.WebDir, Pprof: cfg.Pprof,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	// Clients first, so none is left talking to a stopped session actor.
	return errors.Join(srv.Shutdown(sctx), wsSrv.Shutdown(sctx), sessions.Shutdown(sctx))
}

func mintToken(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mint-token", flag.ContinueOnError)
	session := fs.String("session", "", "session ID (required)")
	user := fs.String("user", "", "user ID (required)")
	role := fs.String("role", string(auth.RolePlayer), "dm, player, or spectator")
	ttl := fs.Duration("ttl", auth.DefaultTTL, "token lifetime")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *session == "" || *user == "" {
		return errors.New("-session and -user are required")
	}
	if !auth.Role(*role).Valid() {
		return fmt.Errorf("unknown role %q", *role)
	}
	secret, err := config.TokenSecret(os.Getenv)
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(secret)
	if err != nil {
		return err
	}
	tok, err := signer.Issue(*session, *user, auth.Role(*role), *ttl)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, tok)
	return err
}

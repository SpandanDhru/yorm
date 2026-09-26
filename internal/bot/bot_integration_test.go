//go:build integration

package bot_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
	"github.com/SpandanDhru/yorm/internal/bot"
	"github.com/SpandanDhru/yorm/internal/db"
	"github.com/SpandanDhru/yorm/internal/db/dbtest"
	"github.com/SpandanDhru/yorm/internal/httpapi"
	"github.com/SpandanDhru/yorm/internal/session"
	"github.com/SpandanDhru/yorm/internal/store"
	"github.com/SpandanDhru/yorm/internal/ws"
)

// TestBotsPlay runs two tables of bots against an in-process server and
// real Postgres: every command is answered, nothing arrives out of order,
// and every bot's state matches the server's view of it.
func TestBotsPlay(t *testing.T) {
	ctx := context.Background()
	url := dbtest.StartPostgres(t)
	if err := db.Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, _ := auth.NewSigner([]byte(strings.Repeat("k", auth.MinKeyLen)))
	st := store.New(pool)
	mgr := session.NewManager(st, log, session.DefaultOptions())
	wsSrv := ws.NewServer(signer, mgr, log, ws.DefaultOptions())
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Log: log, WS: wsSrv, Health: pool.Ping, Signer: signer, Store: st, Sessions: mgr, UploadDir: t.TempDir(),
	}))
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = wsSrv.Shutdown(sctx)
		_ = mgr.Shutdown(sctx)
		srv.Close()
	})

	stats := bot.NewStats()
	run, stop := context.WithCancel(ctx)
	defer stop()
	var clients []*bot.Client
	for range 2 {
		tbl, err := bot.NewTable(run, srv.URL, 5, stats)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, tbl.Clients()...)
		play, done := context.WithTimeout(run, 3*time.Second)
		defer done()
		go tbl.Play(play, 100*time.Millisecond)
	}
	time.Sleep(3500 * time.Millisecond)

	settle, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := bot.Settle(settle, clients); err != nil {
		t.Fatal(err)
	}
	diverged, err := bot.Diverged(ctx, clients)
	if err != nil {
		t.Fatal(err)
	}
	r := stats.Report()
	t.Logf("%+v", r)
	if r.Acks+r.Rejects < 200 || r.Deliveries == 0 {
		t.Fatalf("too little happened: %+v", r)
	}
	if r.Gaps != 0 || r.Errors != 0 || diverged != 0 {
		t.Fatalf("gaps %d, errors %d, diverged %d", r.Gaps, r.Errors, diverged)
	}
}

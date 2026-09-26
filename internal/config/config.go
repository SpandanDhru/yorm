// Package config reads server settings from the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
)

type Config struct {
	Addr           string   // YORM_ADDR, default ":8080"
	DatabaseURL    string   // DATABASE_URL, required
	TokenSecret    []byte   // YORM_TOKEN_SECRET, required, at least 32 bytes
	AllowedOrigins []string // YORM_ALLOWED_ORIGINS, comma-separated WebSocket origin patterns, e.g. "localhost:5173"
	UploadDir      string   // YORM_UPLOAD_DIR, default "data/uploads"
	WebDir         string   // YORM_WEB_DIR, built frontend to serve; unset in development, where Vite serves it
	Pprof          bool     // YORM_PPROF=1 serves /debug/pprof; keep it off where the server is public
	// YORM_DB_MAX_CONNS, Postgres connections; 0 keeps pgx's default (one
	// per CPU, at least 4). Load tests found more connections made things
	// worse on a small Postgres: commits contend for its WAL lock.
	DBMaxConns int32
	// YORM_GROUP_COMMIT, how many writers share appends' transactions (see
	// store.GroupCommit); 0 commits each command on its own. Default 4.
	GroupCommit     int
	ShutdownTimeout time.Duration
}

// Load reads the config through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:            getenv("YORM_ADDR"),
		DatabaseURL:     getenv("DATABASE_URL"),
		UploadDir:       getenv("YORM_UPLOAD_DIR"),
		WebDir:          getenv("YORM_WEB_DIR"),
		Pprof:           getenv("YORM_PPROF") == "1",
		ShutdownTimeout: 10 * time.Second,
	}
	if c.Addr == "" {
		c.Addr = ":8080"
	}
	if c.UploadDir == "" {
		c.UploadDir = "data/uploads"
	}
	c.GroupCommit = 4
	if v := getenv("YORM_GROUP_COMMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("config: YORM_GROUP_COMMIT must be 0 or more")
		}
		c.GroupCommit = n
	}
	if v := getenv("YORM_DB_MAX_CONNS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("config: YORM_DB_MAX_CONNS must be a positive number")
		}
		c.DBMaxConns = int32(n)
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}
	secret, err := TokenSecret(getenv)
	if err != nil {
		return Config{}, err
	}
	c.TokenSecret = secret
	for _, o := range strings.Split(getenv("YORM_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	return c, nil
}

// TokenSecret reads only the signing key, for tools that need no database.
func TokenSecret(getenv func(string) string) ([]byte, error) {
	s := getenv("YORM_TOKEN_SECRET")
	if len(s) < auth.MinKeyLen {
		return nil, fmt.Errorf("config: YORM_TOKEN_SECRET must be at least %d bytes", auth.MinKeyLen)
	}
	return []byte(s), nil
}

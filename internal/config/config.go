// Package config reads server settings from the environment.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SpandanDhru/yorm/internal/auth"
)

type Config struct {
	Addr            string   // YORM_ADDR, default ":8080"
	DatabaseURL     string   // DATABASE_URL, required
	TokenSecret     []byte   // YORM_TOKEN_SECRET, required, at least 32 bytes
	AllowedOrigins  []string // YORM_ALLOWED_ORIGINS, comma-separated WebSocket origin patterns, e.g. "localhost:5173"
	ShutdownTimeout time.Duration
}

// Load reads the config through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:            getenv("YORM_ADDR"),
		DatabaseURL:     getenv("DATABASE_URL"),
		ShutdownTimeout: 10 * time.Second,
	}
	if c.Addr == "" {
		c.Addr = ":8080"
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

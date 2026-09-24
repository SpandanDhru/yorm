package config

import (
	"slices"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

const secret = "0123456789abcdef0123456789abcdef"

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"DATABASE_URL": "postgres://x", "YORM_TOKEN_SECRET": secret}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || string(c.TokenSecret) != secret || c.AllowedOrigins != nil {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestLoadOrigins(t *testing.T) {
	c, err := Load(env(map[string]string{
		"DATABASE_URL":         "postgres://x",
		"YORM_TOKEN_SECRET":    secret,
		"YORM_ALLOWED_ORIGINS": "localhost:5173, yorm.fly.dev,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"localhost:5173", "yorm.fly.dev"}; !slices.Equal(c.AllowedOrigins, want) {
		t.Fatalf("origins = %q, want %q", c.AllowedOrigins, want)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"missing database": {"YORM_TOKEN_SECRET": secret},
		"missing secret":   {"DATABASE_URL": "postgres://x"},
		"short secret":     {"DATABASE_URL": "postgres://x", "YORM_TOKEN_SECRET": "short"},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

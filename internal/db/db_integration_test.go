//go:build integration

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var tables = []string{"sessions", "events", "snapshots", "members"}

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("yorm"),
		postgres.WithUsername("yorm"),
		postgres.WithPassword("yorm"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(time.Minute)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", "public."+name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestMigrateUpDownUp(t *testing.T) {
	ctx := context.Background()
	url := startPostgres(t)

	if err := Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, tbl := range tables {
		if !tableExists(t, pool, tbl) {
			t.Errorf("table %s missing after up", tbl)
		}
	}

	if err := Reset(ctx, url); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		if tableExists(t, pool, tbl) {
			t.Errorf("table %s still present after down", tbl)
		}
	}

	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("second up: %v", err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("up with nothing pending: %v", err)
	}
}

// The persistence design relies on (session_id, seq) rejecting a second
// writer for the same sequence number.
func TestDuplicateSeqIsRejected(t *testing.T) {
	ctx := context.Background()
	url := startPostgres(t)
	if err := Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `INSERT INTO sessions (id, name, invite_code) VALUES ('s1', 'Test', 'abc')`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO events (session_id, seq, name, by_user, data) VALUES ('s1', 1, 'TokenMoved', 'u1', '{}')`
	if _, err := pool.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, insert)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second insert err = %v, want unique_violation", err)
	}
}

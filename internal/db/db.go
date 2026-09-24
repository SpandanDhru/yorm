// Package db opens the Postgres pool and runs migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver that goose needs
	"github.com/pressly/goose/v3"

	"github.com/SpandanDhru/yorm/migrations"
)

// Open connects to Postgres and checks that the database answers.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, url string) error {
	return withProvider(url, func(p *goose.Provider) error {
		_, err := p.Up(ctx)
		return err
	})
}

// Reset rolls back every migration. Used by tests and local development.
func Reset(ctx context.Context, url string) error {
	return withProvider(url, func(p *goose.Provider) error {
		_, err := p.DownTo(ctx, 0)
		return err
	})
}

func withProvider(url string, fn func(*goose.Provider) error) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	if err := fn(p); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

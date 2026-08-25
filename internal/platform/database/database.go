// Package database owns the connection pool and the migration run.
package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// Railway's smaller Postgres plans cap connections well below Postgres' default of
	// 100, and a single API instance has no business claiming all of them. This leaves
	// room for migrations, psql sessions and a second instance during a rolling deploy.
	maxConns = 10

	// Keeping a few connections warm avoids paying TLS and auth setup on the first
	// request after an idle period — which, for this app, is most requests.
	minConns = 2

	maxConnLifetime = time.Hour
	maxConnIdleTime = 30 * time.Minute

	connectTimeout = 10 * time.Second
)

// NewPool opens and verifies the connection pool.
//
// It pings before returning so a bad DATABASE_URL fails at boot with a clear message
// rather than on the first request that needs the database.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	cfg.MaxConns = maxConns
	cfg.MinConns = minConns
	cfg.MaxConnLifetime = maxConnLifetime
	cfg.MaxConnIdleTime = maxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// normalizeURL rewrites the postgresql:// scheme to postgres://.
//
// Both are valid and pgx accepts either, but golang-migrate registers its driver under
// "postgres" alone. Railway hands out postgresql:// URLs, so without this the first
// deploy fails on an unknown driver.
func normalizeURL(databaseURL string) string {
	return strings.Replace(databaseURL, "postgresql://", "postgres://", 1)
}

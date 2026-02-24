package db

import (
	"context"
	"fmt"
	"runtime"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a PostgreSQL connection pool.
// MaxConns is set to runtime.NumCPU() to match the number of Go scheduler threads.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}

	cfg.MaxConns = int32(runtime.NumCPU())

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to db: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return pool, nil
}

package pg

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func WaitForPostgres(ctx context.Context, log *slog.Logger, dbURL string) (*pgxpool.Pool, error) {
	var (
		backoff = time.Second      // 1s
		maxBack = 30 * time.Second // max backoff
	)
	var lastErr error

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("invalid db config: %w", err)
	}

	for {
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingErr := pool.Ping(ctx)
			if pingErr == nil {
				return pool, nil
			}
			lastErr = pingErr
			pool.Close()
		} else {
			lastErr = err
		}

		log.Debug("PostgreSQL not ready yet. Retrying...",
			slog.String("wait", backoff.String()),
			slog.Any("err", lastErr),
			func() slog.Attr {
				if deadline, ok := ctx.Deadline(); ok {
					return slog.String("time_remaining", time.Until(deadline).String())
				}
				return slog.Any("time_remaining", "unknown")
			}(),
		)

		select {
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBack {
				backoff = maxBack
			}
		case <-ctx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf("%w (last db error: %v)", ctx.Err(), lastErr)
			}
			return nil, ctx.Err()
		}
	}
}

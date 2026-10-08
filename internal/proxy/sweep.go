package proxy

import (
	"context"
	"log/slog"
	"time"
)

func Sweeper(ctx context.Context, store *Store, every time.Duration) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := store.Sweep(ctx); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "sweep proxy records", "error", err)
			}
		}
	}
}

package main

import (
	"context"
	"log/slog"
	"time"

	mascotpg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/mascotmedia"
)

// Delete only keys claimed by PostgreSQL. A failed delete is retried at the next sweep.
func cleanMascotUploads(ctx context.Context, repo *mascotpg.Repository, store *mascotmedia.Store, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		keys, err := repo.UnusedUploads(ctx)
		if err != nil {
			logger.Warn("list expired mascot uploads", "error", err)
		}
		for _, key := range keys {
			if ctx.Err() != nil {
				return
			}
			if err := store.Delete(ctx, key); err != nil {
				logger.Warn("delete expired mascot image", "error", err)
				continue
			}
			if err := repo.PurgedUpload(ctx, key); err != nil {
				logger.Warn("finish mascot image cleanup", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

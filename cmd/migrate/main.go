package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/config"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	databaseURL, err := config.LoadDatabaseURL()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	if err := migrations.Run(ctx, conn, "migrations"); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}

	logger.Info("migrations are up to date")
}

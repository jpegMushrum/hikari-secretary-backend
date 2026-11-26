package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	mascotpg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/adapters/postgres"
	santapg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/adapters/postgres"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/config"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/database"
)

// Commands available to the operator of the backend container.
func runSeasonCommand(args []string, logger *slog.Logger) error {
	var closeID int64
	switch {
	case len(args) == 1 && (args[0] == "santa-start" || args[0] == "mascot-start"):
	case len(args) == 2 && (args[0] == "santa-close" || args[0] == "mascot-close"):
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || id <= 0 {
			return errors.New("closing a season requires a positive season ID")
		}
		closeID = id
	default:
		return errors.New("usage: api {santa,mascot}-start | {santa,mascot}-close SEASON_ID")
	}
	url, err := config.LoadDatabaseURL()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if strings.HasPrefix(args[0], "mascot-") {
		repo := mascotpg.NewRepository(pool)
		if closeID > 0 {
			return repo.Close(ctx, closeID)
		}
		season, err := repo.Start(ctx)
		if err == nil {
			logger.Info("Mascot season active", "season_id", season.ID)
		}
		return err
	}
	service := santapg.NewRepository(pool, nil)
	if closeID > 0 {
		if err := service.Close(ctx, closeID); err != nil {
			return err
		}
		logger.Info("Santa season closed", "season_id", closeID)
		return nil
	}
	season, err := service.Start(ctx)
	if err != nil {
		return err
	}
	logger.Info("Santa season active", "season_id", season.ID, "status", season.Status)
	return nil
}

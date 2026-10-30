package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	peoplepostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/adapters/postgres"
	peopleapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/config"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/database"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/httpapi"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/serviceauth"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	peopleService := peopleapp.NewService(peoplepostgres.NewRepository(pool))

	server := &http.Server{
		Addr: cfg.HTTPAddress,
		Handler: httpapi.NewRouter(
			pool,
			telegramauth.NewValidator(cfg.TelegramBotToken, cfg.TelegramInitDataMaxAge),
			cfg.AdminTelegramIDs,
			serviceauth.NewValidator(cfg.BotServiceToken),
			peopleService,
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	errorsCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server started", "address", cfg.HTTPAddress, "environment", cfg.Environment)
		errorsCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	if err := <-errorsCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server stopped with an error", "error", err)
		os.Exit(1)
	}

	logger.Info("HTTP server stopped")
}

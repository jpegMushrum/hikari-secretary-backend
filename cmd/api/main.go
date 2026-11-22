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
	_ "time/tzdata"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/adminapi"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/botapi"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/adapters/memberapi"
	eventspostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/adapters/postgres"
	eventsapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/events/application"
	interactionspostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/adapters/postgres"
	interactionsapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/application"
	learningcontent "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent"
	peoplepostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/adapters/postgres"
	peopleapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	publishingpostgres "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/adapters/postgres"
	publishingapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
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
	interactionService := interactionsapp.NewService(interactionspostgres.NewRepository(pool))
	eventsService := eventsapp.NewService(eventspostgres.NewRepository(pool))
	publishingService := publishingapp.NewService(publishingpostgres.NewRepository(pool))
	var aiService *learningcontent.Service
	location, err := time.LoadLocation(cfg.AI.Timezone)
	if err != nil {
		logger.Error("invalid AI timezone", "error", err)
		os.Exit(1)
	}
	if cfg.AI.Enabled {
		provider := learningcontent.NewDeepSeekProvider(cfg.AI.APIKey, cfg.AI.Model, cfg.AI.BaseURL, cfg.AI.Timeout, cfg.AI.QuestionCount, cfg.AI.VocabularyCount, cfg.AI.GrammarCount)
		repository := learningcontent.NewPostgresRepository(pool)
		aiService = learningcontent.NewService(provider, repository, domain.Target{Key: cfg.AI.Target.Key, Name: cfg.AI.Target.Name, Destination: cfg.AI.Target.Destination, MessageThreadID: cfg.AI.Target.MessageThreadID}, cfg.AI.QuestionCount, cfg.AI.VocabularyCount, cfg.AI.GrammarCount)
		creator := int64(0)
		for id := range cfg.AdminTelegramIDs {
			if creator == 0 || id < creator {
				creator = id
			}
		}
		go learningcontent.NewWorker(repository, aiService, location, cfg.AI.PublishTime, creator, cfg.AI.MaxAttempts, cfg.AI.Timeout, logger).Run(ctx)
		logger.Info("AI learning content enabled", "target", cfg.AI.Target.Key, "automatic", cfg.AI.PublishTime != "")
	} else if err := learningcontent.NewPostgresRepository(pool).FailUnavailableManualGenerations(ctx); err != nil {
		logger.Error("release publications awaiting disabled generator", "error", err)
		os.Exit(1)
	}
	if err := publishingService.RecoverInterruptedDeliveries(ctx); err != nil {
		logger.Error("recover interrupted publication deliveries", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr: cfg.HTTPAddress,
		Handler: httpapi.NewRouter(
			pool,
			telegramauth.NewValidator(cfg.TelegramBotToken, cfg.TelegramInitDataMaxAge),
			cfg.AdminTelegramIDs,
			serviceauth.NewValidator(cfg.BotServiceToken),
			peopleService,
			botapi.NewRouter(pool, interactionService, publishingService, aiService),
			memberapi.NewRouter(pool, cfg.ReminderOptions...),
			(&adminapi.Management{Pool: pool, Publications: publishingService, AIEnabled: cfg.AI.Enabled,
				AIAutomationConfigured: cfg.AI.Enabled && cfg.AI.PublishTime != "", AILocation: location, AIPublishTime: cfg.AI.PublishTime}).Router(adminapi.NewRouter(eventsService)),
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Manual AI generation can legitimately take up to the configured
		// provider timeout. Keep the HTTP connection alive for that operation.
		WriteTimeout:   120 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
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

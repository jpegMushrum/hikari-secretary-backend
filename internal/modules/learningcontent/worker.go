package learningcontent

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Worker struct {
	repo        *PostgresRepository
	service     *Service
	location    *time.Location
	publishTime string
	creatorID   int64
	maxAttempts int
	logger      *slog.Logger
}

func NewWorker(repo *PostgresRepository, service *Service, location *time.Location, publishTime string, creatorID int64, maxAttempts int, logger *slog.Logger) *Worker {
	return &Worker{repo, service, location, publishTime, creatorID, maxAttempts, logger}
}
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		w.cycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w *Worker) cycle(ctx context.Context) {
	now := time.Now().In(w.location)
	parsed, err := time.Parse("15:04", w.publishTime)
	if err != nil {
		w.logger.Error("invalid AI publish time", "error", err)
		return
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, w.location)
	if _, err = w.repo.Enqueue(ctx, today.UTC()); err != nil {
		w.logger.Error("enqueue AI topic", "error", err)
	}
	if !today.After(now) {
		if _, err = w.repo.Enqueue(ctx, today.AddDate(0, 0, 1).UTC()); err != nil {
			w.logger.Error("enqueue AI topic", "error", err)
		}
	}
	job, err := w.repo.Claim(ctx)
	if err != nil {
		w.logger.Error("claim AI topic", "error", err)
		return
	}
	if job == nil {
		return
	}
	topic, err := w.service.Generate(ctx, nil, nil)
	if err == nil {
		var publicationID int64
		publicationID, err = w.service.Schedule(ctx, topic, w.creatorID, maxTime(job.ScheduledFor, time.Now().UTC()), fmt.Sprintf("daily-topic-job:%d", job.ID), false)
		if err == nil {
			err = w.repo.MarkScheduled(ctx, job.ID, publicationID, topic)
		}
	}
	if err == nil {
		w.logger.Info("AI topic scheduled", "job_id", job.ID)
		return
	}
	var retry *time.Time
	if job.Attempts < w.maxAttempts {
		value := time.Now().UTC().Add(time.Duration(min(5*(1<<(job.Attempts-1)), 360)) * time.Minute)
		retry = &value
	}
	_ = w.repo.MarkFailed(ctx, job.ID, err.Error(), retry)
	w.logger.Error("AI topic generation failed", "job_id", job.ID, "attempt", job.Attempts, "error", err)
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

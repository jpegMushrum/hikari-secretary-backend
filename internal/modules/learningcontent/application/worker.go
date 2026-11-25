package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type JobRepository interface {
	Enqueue(context.Context, time.Time) (int64, error)
	Claim(context.Context, bool, time.Duration) (*content.Job, error)
	Complete(context.Context, *content.Job, domain.ScheduledDraft, content.Topic) error
	MarkFailed(context.Context, *content.Job, string, *time.Time) error
}

type Worker struct {
	repo        JobRepository
	service     *Service
	location    *time.Location
	publishTime string
	creatorID   int64
	maxAttempts int
	lease       time.Duration
	logger      *slog.Logger
}

func NewWorker(repo JobRepository, service *Service, location *time.Location, publishTime string, creatorID int64, maxAttempts int, providerTimeout time.Duration, logger *slog.Logger) *Worker {
	// DeepSeek may retry one truncated response within the same generation.
	return &Worker{repo, service, location, publishTime, creatorID, maxAttempts, 2*providerTimeout + time.Minute, logger}
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
	if w.publishTime != "" {
		w.enqueueDaily(ctx)
	}
	w.processDue(ctx)
}

func (w *Worker) enqueueDaily(ctx context.Context) {
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
}

func (w *Worker) processDue(ctx context.Context) {
	job, err := w.repo.Claim(ctx, w.publishTime != "", w.lease)
	if err != nil {
		w.logger.Error("claim AI topic", "error", err)
		return
	}
	if job == nil {
		return
	}
	topic, err := w.service.Generate(ctx, nil)
	if err == nil {
		creator := w.creatorID
		if job.RequestedBy != nil {
			creator = *job.RequestedBy
		}
		var draft domain.ScheduledDraft
		draft, err = w.service.scheduledDraft(topic, creator, maxTime(job.ScheduledFor, time.Now().UTC()), fmt.Sprintf("daily-topic-job:%d", job.ID))
		if err == nil {
			err = w.repo.Complete(ctx, job, draft, topic)
		}
	}
	if err == nil {
		w.logger.Info("AI topic scheduled", "job_id", job.ID)
		return
	}
	var retry *time.Time
	if job.RequestedBy == nil && job.Attempts < w.maxAttempts && !errors.Is(err, domain.ErrInvalidState) {
		value := time.Now().UTC().Add(time.Duration(min(5*(1<<(job.Attempts-1)), 360)) * time.Minute)
		retry = &value
	}
	if markErr := w.repo.MarkFailed(ctx, job, err.Error(), retry); markErr != nil {
		w.logger.Error("record AI generation failure", "job_id", job.ID, "error", markErr)
	}
	w.logger.Error("AI topic generation failed", "job_id", job.ID, "attempt", job.Attempts, "error", err)
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

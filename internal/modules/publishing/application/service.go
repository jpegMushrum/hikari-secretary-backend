package application

import (
	"context"
	"fmt"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type Repository interface {
	CreateDraft(context.Context, domain.Draft) (int64, error)
	CreateScheduled(context.Context, domain.ScheduledDraft) (int64, error)
	ToggleTarget(context.Context, int64, domain.Target) error
	Schedule(context.Context, int64, time.Time) (bool, error)
	Publication(context.Context, int64) (map[string]any, bool, error)
	Queue(context.Context, int) ([]map[string]any, error)
	Cancel(context.Context, int64) (bool, error)
	ClaimDue(context.Context) (*domain.Delivery, error)
	Succeeded(context.Context, int64, string) error
	Failed(context.Context, int64, string, int) error
	Notices(context.Context) ([]domain.Notice, error)
	MarkNotified(context.Context, int64) error
	RecoverInterruptedDeliveries(context.Context) error
	ReplaceScheduled(context.Context, int64, string, map[string]any) (bool, error)
}

func (s *Service) Publication(ctx context.Context, id int64) (map[string]any, bool, error) {
	return s.repository.Publication(ctx, id)
}
func (s *Service) Queue(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit < 1 {
		limit = 20
	}
	return s.repository.Queue(ctx, limit)
}
func (s *Service) Cancel(ctx context.Context, id int64) (bool, error) {
	return s.repository.Cancel(ctx, id)
}
func (s *Service) ClaimDue(ctx context.Context) (*domain.Delivery, error) {
	return s.repository.ClaimDue(ctx)
}
func (s *Service) Succeeded(ctx context.Context, id int64, externalID string) error {
	return s.repository.Succeeded(ctx, id, externalID)
}
func (s *Service) Failed(ctx context.Context, id int64, reason string, maxAttempts int) error {
	if maxAttempts < 1 {
		return fmt.Errorf("invalid max attempts")
	}
	return s.repository.Failed(ctx, id, reason, maxAttempts)
}
func (s *Service) Notices(ctx context.Context) ([]domain.Notice, error) {
	return s.repository.Notices(ctx)
}
func (s *Service) MarkNotified(ctx context.Context, id int64) error {
	return s.repository.MarkNotified(ctx, id)
}

type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }
func (s *Service) CreateDraft(ctx context.Context, draft domain.Draft) (int64, error) {
	if err := validateDraft(&draft); err != nil {
		return 0, err
	}
	return s.repository.CreateDraft(ctx, draft)
}

func (s *Service) CreateScheduled(ctx context.Context, scheduled domain.ScheduledDraft) (int64, error) {
	if err := validateDraft(&scheduled.Draft); err != nil {
		return 0, err
	}
	if scheduled.ScheduledAt.IsZero() || len(scheduled.Targets) == 0 {
		return 0, fmt.Errorf("schedule and at least one target are required")
	}
	for _, target := range scheduled.Targets {
		if target.Key == "" || target.Name == "" || target.Destination == "" {
			return 0, fmt.Errorf("invalid target")
		}
	}
	scheduled.ScheduledAt = scheduled.ScheduledAt.UTC()
	return s.repository.CreateScheduled(ctx, scheduled)
}

func (s *Service) RecoverInterruptedDeliveries(ctx context.Context) error {
	return s.repository.RecoverInterruptedDeliveries(ctx)
}

func (s *Service) ReplaceScheduled(ctx context.Context, id int64, text string, rich map[string]any) (bool, error) {
	if id <= 0 {
		return false, fmt.Errorf("publication id is required")
	}
	return s.repository.ReplaceScheduled(ctx, id, text, rich)
}

func validateDraft(draft *domain.Draft) error {
	if draft.CreatorTelegramID <= 0 {
		return fmt.Errorf("creator telegram id is required")
	}
	if draft.Source == "" {
		draft.Source = "admin"
	}
	for index, media := range draft.Media {
		if media.Position != index || media.Path == "" {
			return fmt.Errorf("invalid media")
		}
	}
	return nil
}
func (s *Service) ToggleTarget(ctx context.Context, publicationID int64, target domain.Target) error {
	if publicationID <= 0 || target.Key == "" || target.Name == "" || target.Destination == "" {
		return fmt.Errorf("invalid target")
	}
	return s.repository.ToggleTarget(ctx, publicationID, target)
}
func (s *Service) Schedule(ctx context.Context, publicationID int64, when time.Time) (bool, error) {
	if publicationID <= 0 {
		return false, fmt.Errorf("publication id is required")
	}
	return s.repository.Schedule(ctx, publicationID, when.UTC())
}

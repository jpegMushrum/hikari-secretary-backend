package application

import (
	"context"
	"fmt"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type Repository interface {
	CreateDraft(context.Context, domain.Draft) (int64, error)
}
type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }
func (s *Service) CreateDraft(ctx context.Context, draft domain.Draft) (int64, error) {
	if draft.CreatorTelegramID <= 0 {
		return 0, fmt.Errorf("creator telegram id is required")
	}
	if draft.Source == "" {
		draft.Source = "admin"
	}
	for index, media := range draft.Media {
		if media.Position != index || media.Path == "" {
			return 0, fmt.Errorf("invalid media")
		}
	}
	return s.repository.CreateDraft(ctx, draft)
}

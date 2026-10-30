package application

import (
	"context"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
)

type SortField string

const (
	SortByName      SortField = "name"
	SortByVisits    SortField = "visits"
	SortByLastVisit SortField = "last_visit"
)

type SortOrder string

const (
	SortAscending  SortOrder = "asc"
	SortDescending SortOrder = "desc"
)

type ListPeopleQuery struct {
	Page             pagination.Page
	Search           string
	IsRussianCitizen *bool
	IsITMOStudent    *bool
	Sort             SortField
	Order            SortOrder
}

type PersonListItem struct {
	ID               int64      `json:"id"`
	FullName         *string    `json:"full_name"`
	TelegramUserID   int64      `json:"telegram_user_id"`
	Username         *string    `json:"username"`
	DisplayName      string     `json:"display_name"`
	IsRussianCitizen *bool      `json:"is_russian_citizen"`
	IsITMOStudent    *bool      `json:"is_itmo_student"`
	Visits           int64      `json:"visits"`
	FeedbackCount    int64      `json:"feedback_count"`
	LastVisitAt      *time.Time `json:"last_visit_at"`
}

type ListPeopleResult struct {
	Items      []PersonListItem
	TotalItems int64
}

type Repository interface {
	SyncTelegramIdentity(context.Context, domain.SyncIdentityCommand) (domain.TelegramIdentity, error)
	ListPeople(context.Context, ListPeopleQuery) (ListPeopleResult, error)
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (s *Service) SyncTelegramIdentity(ctx context.Context, command domain.SyncIdentityCommand) (domain.TelegramIdentity, error) {
	normalized, err := domain.NormalizeSyncIdentity(command)
	if err != nil {
		return domain.TelegramIdentity{}, err
	}
	return s.repository.SyncTelegramIdentity(ctx, normalized)
}

func (s *Service) ListPeople(ctx context.Context, query ListPeopleQuery) (ListPeopleResult, error) {
	return s.repository.ListPeople(ctx, query)
}

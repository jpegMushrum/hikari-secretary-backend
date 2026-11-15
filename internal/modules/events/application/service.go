package application

import (
	"context"
	"errors"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
)

var (
	ErrEventNotFound  = errors.New("event not found")
	ErrEventNotActive = errors.New("event not active")
)

type SortField string

const (
	SortByStartsAt  SortField = "starts_at"
	SortByTitle     SortField = "title"
	SortByCreatedAt SortField = "created_at"
)

type SortOrder string

const (
	SortAscending  SortOrder = "asc"
	SortDescending SortOrder = "desc"
)

type ListEventsQuery struct {
	Page                 pagination.Page
	Status               string
	Search               string
	StartsFrom, StartsTo *time.Time
	Sort                 SortField
	Order                SortOrder
}

type Event struct {
	ID                     int64     `json:"id"`
	PublicationID          *int64    `json:"publication_id"`
	Title                  string    `json:"title"`
	StartsAt               time.Time `json:"starts_at"`
	EndsAt                 time.Time `json:"ends_at"`
	Status                 string    `json:"status"`
	Published              bool      `json:"published"`
	Registrations          int64     `json:"registrations"`
	CancelledRegistrations int64     `json:"cancelled_registrations"`
	Attended               int64     `json:"attended"`
	CreatedAt              time.Time `json:"created_at"`
}
type ListEventsResult struct {
	Items      []Event
	TotalItems int64
}

type ParticipantSort string

const (
	ParticipantByName         ParticipantSort = "name"
	ParticipantByRegisteredAt ParticipantSort = "registered_at"
	ParticipantByNeedsPass    ParticipantSort = "needs_pass"
)

type ListParticipantsQuery struct {
	Page                                       pagination.Page
	Status                                     string
	IsRussianCitizen, IsITMOStudent, NeedsPass *bool
	Sort                                       ParticipantSort
	Order                                      SortOrder
}
type Participant struct {
	PersonID         int64      `json:"person_id"`
	TelegramUserID   int64      `json:"telegram_user_id"`
	FullName         string     `json:"full_name"`
	Username         *string    `json:"username"`
	RegisteredAt     time.Time  `json:"registered_at"`
	CancelledAt      *time.Time `json:"cancelled_at"`
	Status           string     `json:"status"`
	Attended         *bool      `json:"attended"`
	IsRussianCitizen *bool      `json:"is_russian_citizen"`
	IsITMOStudent    *bool      `json:"is_itmo_student"`
	NeedsPass        bool       `json:"needs_pass"`
}
type ListParticipantsResult struct {
	Items      []Participant
	TotalItems int64
}
type CancelResult struct {
	Event               Event `json:"event"`
	NotificationsQueued int64 `json:"notifications_queued"`
}

type Repository interface {
	List(context.Context, ListEventsQuery) (ListEventsResult, error)
	Get(context.Context, int64) (Event, error)
	ListParticipants(context.Context, int64, ListParticipantsQuery) (ListParticipantsResult, error)
	Cancel(context.Context, int64, int64) (CancelResult, error)
}
type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository} }
func (s *Service) List(ctx context.Context, q ListEventsQuery) (ListEventsResult, error) {
	return s.repository.List(ctx, q)
}
func (s *Service) Get(ctx context.Context, id int64) (Event, error) {
	if id <= 0 {
		return Event{}, ErrEventNotFound
	}
	return s.repository.Get(ctx, id)
}
func (s *Service) ListParticipants(ctx context.Context, id int64, q ListParticipantsQuery) (ListParticipantsResult, error) {
	if id <= 0 {
		return ListParticipantsResult{}, ErrEventNotFound
	}
	return s.repository.ListParticipants(ctx, id, q)
}
func (s *Service) Cancel(ctx context.Context, id, actorTelegramID int64) (CancelResult, error) {
	if id <= 0 {
		return CancelResult{}, ErrEventNotFound
	}
	return s.repository.Cancel(ctx, id, actorTelegramID)
}

package application

import (
	"context"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa/domain"
)

type Recipient struct {
	PersonID   int64   `json:"person_id"`
	Name       string  `json:"name"`
	Wishes     string  `json:"wishes"`
	TelegramID int64   `json:"telegram_id"`
	Username   *string `json:"username"`
}

type AdminParticipant struct {
	domain.Participant
	TelegramID int64   `json:"telegram_id"`
	Username   *string `json:"username"`
}

type AdminView struct {
	Season       domain.Season      `json:"season"`
	Participants []AdminParticipant `json:"participants"`
	Pairs        []domain.Pair      `json:"pairs"`
}

type MemberView struct {
	Season        domain.Season       `json:"season"`
	Participation *domain.Participant `json:"participation"`
	Recipient     *Recipient          `json:"recipient"`
	DraftWishes   string              `json:"draft_wishes"`
}

type AdminCommand struct {
	Action            string        `json:"action"`
	SeasonID          int64         `json:"season_id"`
	Revision          int64         `json:"revision"`
	Budget            string        `json:"budget"`
	ExchangeAt        *time.Time    `json:"exchange_at"`
	RegistrationUntil *time.Time    `json:"registration_until"`
	ReminderMinutes   int           `json:"reminder_minutes"`
	Pairs             []domain.Pair `json:"pairs"`
	PersonID          int64         `json:"person_id"`
	ActorTelegramID   int64         `json:"-"`
}

type MemberCommand struct {
	RecipientID     int64   `json:"recipient_id"`
	Action          string  `json:"action"`
	SeasonID        int64   `json:"season_id"`
	Wishes          string  `json:"wishes"`
	Attendance      string  `json:"attendance"`
	Gift            string  `json:"gift"`
	GiftDescription *string `json:"gift_description"`
}

// Service exposes the season operations shared by the administrative and member APIs.
type Service interface {
	VisibleSeason(ctx context.Context, admin bool) (*domain.Season, error)
	Admin(ctx context.Context) (AdminView, error)
	AdminAction(ctx context.Context, command AdminCommand) error
	Member(ctx context.Context, personID int64) (MemberView, error)
	MemberAction(ctx context.Context, personID int64, command MemberCommand) error
	Reset(ctx context.Context, seasonID, revision, actor int64) error
}

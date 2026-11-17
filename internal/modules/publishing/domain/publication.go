package domain

import (
	"errors"
	"time"
)

var ErrPendingPublicationLimit = errors.New("pending publication limit reached")
var ErrInvalidState = errors.New("publication is not a draft")

type Status string

const (
	StatusDraft     Status = "draft"
	StatusScheduled Status = "scheduled"
	StatusSent      Status = "sent"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Media struct {
	Position int
	Path     string
}
type Target struct {
	Key, Name, Destination string
	MessageThreadID        *int64
}
type Draft struct {
	ID                int64 // Nonzero replaces an existing draft under a row lock.
	Targets           []Target
	CreatorTelegramID int64
	Text              string
	Entities          []map[string]any
	RichMessage       map[string]any
	Media             []Media
	Source            string
	SourceKey         string
}

// ScheduledDraft is the atomic command used by workers that do not need an
// interactive draft step. Keeping it as one command prevents a retry from
// creating a draft while toggling an already-created target back off.
type ScheduledDraft struct {
	Draft
	Targets     []Target
	ScheduledAt time.Time
}
type Publication struct {
	ID                int64
	CreatorTelegramID int64
	Text              string
	Status            Status
	ScheduledAt       *time.Time
}

type Delivery struct {
	ID                int64            `json:"id"`
	PublicationID     int64            `json:"publication_id"`
	CreatorTelegramID int64            `json:"creator_telegram_id"`
	TargetKey         string           `json:"target_key"`
	TargetName        string           `json:"target_name"`
	Destination       string           `json:"destination"`
	MessageThreadID   *int64           `json:"message_thread_id"`
	Text              string           `json:"text"`
	Entities          []map[string]any `json:"entities"`
	RichMessage       map[string]any   `json:"rich_message"`
	MediaPaths        []string         `json:"media_paths"`
	Attempts          int              `json:"attempts"`
	EventEnabled      bool             `json:"event_enabled"`
}

type Notice struct {
	ID                int64            `json:"id"`
	CreatorTelegramID int64            `json:"creator_id"`
	Status            Status           `json:"status"`
	Deliveries        []DeliveryResult `json:"deliveries"`
}
type DeliveryResult struct {
	TargetName string `json:"target_name"`
	Status     string `json:"status"`
	LastError  string `json:"last_error"`
}

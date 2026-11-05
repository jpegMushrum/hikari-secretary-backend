package domain

import "time"

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
type Draft struct {
	CreatorTelegramID int64
	Text              string
	Entities          []map[string]any
	RichMessage       map[string]any
	Media             []Media
	Source            string
	SourceKey         string
}
type Publication struct {
	ID                int64
	CreatorTelegramID int64
	Text              string
	Status            Status
	ScheduledAt       *time.Time
}

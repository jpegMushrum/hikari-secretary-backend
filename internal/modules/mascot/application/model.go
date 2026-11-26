package application

import (
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
)

type Visibility struct {
	Visible         bool       `json:"visible"`
	SubmissionUntil *time.Time `json:"submission_until,omitempty"`
}

type Command struct {
	Action      string         `json:"action"`
	SeasonID    int64          `json:"season_id"`
	Round       int            `json:"round"`
	Deadline    string         `json:"deadline"`
	EntryID     int64          `json:"entry_id"`
	EntryIDs    []int64        `json:"entry_ids"`
	PersonID    int64          `json:"person_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Images      []domain.Image `json:"images"`
	RequestID   string         `json:"request_id"`
	Approved    *bool          `json:"approved"`
	Like        *bool          `json:"like"`
}

type Entry struct {
	ID               int64          `json:"id"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Images           []domain.Image `json:"images"`
	Approved         bool           `json:"approved"`
	Author           string         `json:"author,omitempty"`
	AuthorTelegramID *int64         `json:"author_telegram_id,omitempty"`
	AuthorUsername   string         `json:"author_username,omitempty"`
}

type Voter struct {
	PersonID   int64  `json:"person_id"`
	Name       string `json:"name"`
	Excluded   bool   `json:"excluded"`
	TelegramID *int64 `json:"telegram_id,omitempty"`
	Username   string `json:"username,omitempty"`
}

type Result struct {
	EntryID int64    `json:"entry_id"`
	Name    string   `json:"name"`
	Place   int      `json:"place"`
	Likes   int      `json:"likes"`
	Skips   int      `json:"skips"`
	Percent *float64 `json:"percent"`
}

type Round struct {
	Number    int      `json:"number"`
	Rows      []Result `json:"rows"`
	Cancelled bool     `json:"cancelled"`
}

type View struct {
	Season        domain.Season `json:"season"`
	Eligible      bool          `json:"eligible"`
	Ready         bool          `json:"ready"`
	CanReady      bool          `json:"can_ready"`
	Entries       []Entry       `json:"entries"`
	Voters        []Voter       `json:"voters"`
	Card          *Entry        `json:"card"`
	Remaining     int           `json:"remaining"`
	Rounds        []Round       `json:"rounds"`
	Finalists     []int64       `json:"finalists"`
	PendingImages []string      `json:"pending_images"`
}

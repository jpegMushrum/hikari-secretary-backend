package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalid   = errors.New("invalid mascot command")
	ErrConflict  = errors.New("mascot state changed")
	ErrNotFound  = errors.New("mascot not found")
	ErrForbidden = errors.New("mascot access denied")
)

type Season struct {
	ID       int64      `json:"id"`
	Phase    string     `json:"phase"`
	Deadline *time.Time `json:"deadline"`
	Round    int        `json:"round"`
	WinnerID *int64     `json:"winner_id"`
}

type Image struct {
	Key string `json:"key"`
	AI  bool   `json:"ai"`
}

package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("Santa season or participant not available")
	ErrInvalid  = errors.New("invalid Santa request")
	ErrConflict = errors.New("Santa state changed or action unavailable")
)

type Season struct {
	ID                int64      `json:"id"`
	Status            string     `json:"status"`
	Budget            string     `json:"budget"`
	ExchangeAt        *time.Time `json:"exchange_at"`
	RegistrationUntil *time.Time `json:"registration_until"`
	ReminderMinutes   int        `json:"reminder_minutes"`
	Revision          int64      `json:"revision"`
	Confirmed         bool       `json:"confirmed"`
	ConfirmedRevision int64      `json:"-"`
}

type Participant struct {
	PersonID        int64  `json:"person_id"`
	Name            string `json:"name"`
	Wishes          string `json:"wishes"`
	Status          string `json:"status"`
	Attendance      string `json:"attendance"`
	Gift            string `json:"gift"`
	GiftDescription string `json:"gift_description"`
}

type Pair struct {
	GiverID     int64 `json:"giver_id"`
	RecipientID int64 `json:"recipient_id"`
}

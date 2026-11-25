package domain

import "time"

type Job struct {
	ID            int64
	ScheduledFor  time.Time
	Attempts      int
	PublicationID *int64
	RequestedBy   *int64
	LeaseUntil    time.Time
}

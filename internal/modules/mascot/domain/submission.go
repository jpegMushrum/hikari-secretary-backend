package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// SubmissionDeadline is the first instant of the selected Moscow calendar day.
func SubmissionDeadline(date string, now time.Time) (time.Time, error) {
	value, err := time.ParseInLocation("2006-01-02", date, time.FixedZone("MSK", 3*3600))
	if err != nil || !value.After(now) {
		return time.Time{}, ErrInvalid
	}
	return value, nil
}

func ValidateConcept(name, description string, images []Image) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 120 || strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > 2000 || len(images) < 1 || len(images) > 3 {
		return ErrInvalid
	}
	used := map[string]bool{}
	for _, image := range images {
		if image.Key == "" || used[image.Key] {
			return ErrInvalid
		}
		used[image.Key] = true
	}
	return nil
}

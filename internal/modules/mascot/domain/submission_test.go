package domain

import (
	"testing"
	"time"
)

func TestSubmissionDeadlineIsMoscowMidnight(t *testing.T) {
	end := time.Date(2026, 12, 14, 21, 0, 0, 0, time.UTC)
	got, err := SubmissionDeadline("2026-12-15", end.Add(-time.Nanosecond))
	if err != nil || !got.Equal(end) {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := SubmissionDeadline("2026-12-15", end); err != ErrInvalid {
		t.Fatal("deadline already reached")
	}
}

func TestDistinctConceptImages(t *testing.T) {
	if ValidateConcept("狐", "Рисунок", []Image{{Key: "a", AI: true}, {Key: "b"}}) != nil {
		t.Fatal("valid concept rejected")
	}
	if ValidateConcept("狐", "Рисунок", []Image{{Key: "a", AI: true}, {Key: "a"}}) != ErrInvalid {
		t.Fatal("duplicate image admitted")
	}
}

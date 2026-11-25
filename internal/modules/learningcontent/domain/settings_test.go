package domain

import (
	"strings"
	"testing"
)

func TestSettingsValidation(t *testing.T) {
	for _, level := range []string{"N1", "N2", "N3", "N4", "N5"} {
		comment := strings.Repeat("あ", 500)
		if err := (Settings{level, &comment}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, level := range []string{"", "N6", "n3"} {
		if (Settings{JLPTLevel: level}).Validate() == nil {
			t.Fatalf("accepted %q", level)
		}
	}
	comment := strings.Repeat("あ", 501)
	if (Settings{"N3", &comment}).Validate() == nil {
		t.Fatal("accepted oversized comment")
	}
}

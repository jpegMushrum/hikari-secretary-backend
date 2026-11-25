package domain

import "testing"

func TestTopicValidationRequiresConfiguredCounts(t *testing.T) {
	topic := Topic{JLPTLevel: "N3", Title: "題", VocabularyTheme: "語", Introduction: "文", Questions: []string{"質問"}}
	if err := topic.Validate(1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := topic.Validate(2, 0, 0); err == nil {
		t.Fatal("expected count validation error")
	}
}

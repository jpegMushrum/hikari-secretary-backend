package learningcontent

import (
	"fmt"
	"strings"
)

type VocabularyItem struct {
	Japanese    string `json:"japanese"`
	Reading     string `json:"reading"`
	Translation string `json:"translation"`
}

type GrammarPoint struct {
	Pattern     string `json:"pattern"`
	Explanation string `json:"explanation"`
	Example     string `json:"example"`
	Translation string `json:"translation"`
}

type Topic struct {
	JLPTLevel       string           `json:"jlpt_level"`
	Title           string           `json:"title"`
	VocabularyTheme string           `json:"vocabulary_theme"`
	Introduction    string           `json:"introduction"`
	Questions       []string         `json:"questions"`
	Vocabulary      []VocabularyItem `json:"vocabulary"`
	Grammar         []GrammarPoint   `json:"grammar"`
}

type Options struct {
	JLPTLevel    string  `json:"jlpt_level"`
	AdminComment *string `json:"admin_comment"`
}

func (t Topic) Validate(questionCount, vocabularyCount, grammarCount int) error {
	levels := map[string]bool{"N5": true, "N4": true, "N3": true, "N2": true, "N1": true}
	if !levels[t.JLPTLevel] {
		return fmt.Errorf("invalid JLPT level")
	}
	if strings.TrimSpace(t.Title) == "" || len([]rune(t.Title)) > 160 {
		return fmt.Errorf("invalid title")
	}
	if strings.TrimSpace(t.VocabularyTheme) == "" || len([]rune(t.VocabularyTheme)) > 160 {
		return fmt.Errorf("invalid vocabulary theme")
	}
	if strings.TrimSpace(t.Introduction) == "" {
		return fmt.Errorf("introduction is required")
	}
	if len(t.Questions) != questionCount {
		return fmt.Errorf("got %d questions, expected %d", len(t.Questions), questionCount)
	}
	if len(t.Vocabulary) != vocabularyCount {
		return fmt.Errorf("got %d vocabulary items, expected %d", len(t.Vocabulary), vocabularyCount)
	}
	if len(t.Grammar) != grammarCount {
		return fmt.Errorf("got %d grammar points, expected %d", len(t.Grammar), grammarCount)
	}
	for _, value := range t.Questions {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("empty question")
		}
	}
	for _, value := range t.Vocabulary {
		if strings.TrimSpace(value.Japanese) == "" || strings.TrimSpace(value.Reading) == "" || strings.TrimSpace(value.Translation) == "" {
			return fmt.Errorf("incomplete vocabulary item")
		}
	}
	for _, value := range t.Grammar {
		if strings.TrimSpace(value.Pattern) == "" || strings.TrimSpace(value.Explanation) == "" || strings.TrimSpace(value.Example) == "" || strings.TrimSpace(value.Translation) == "" {
			return fmt.Errorf("incomplete grammar point")
		}
	}
	return nil
}

func normalize(value string) string { return strings.Join(strings.Fields(strings.ToLower(value)), " ") }

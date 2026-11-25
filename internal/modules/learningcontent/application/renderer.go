package application

import (
	"fmt"
	"strings"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
)

func Render(topic content.Topic) (string, map[string]any) {
	questions := make([]string, 0, len(topic.Questions))
	for _, q := range topic.Questions {
		questions = append(questions, "• "+q)
	}
	vocabulary := make([]string, 0, len(topic.Vocabulary))
	for _, item := range topic.Vocabulary {
		vocabulary = append(vocabulary, fmt.Sprintf("• %s（%s）— %s", item.Japanese, item.Reading, item.Translation))
	}
	grammar := make([]string, 0, len(topic.Grammar))
	for _, item := range topic.Grammar {
		grammar = append(grammar, fmt.Sprintf("• %s — %s\n  %s\n  %s", item.Pattern, item.Explanation, item.Example, item.Translation))
	}
	text := fmt.Sprintf("🗣 Тема дня: %s\n\n%s\n\n💬 Вопросы для обсуждения:\n%s\n\n📚 Полезная лексика:\n%s\n\n🧩 Грамматика:\n%s", topic.Title, topic.Introduction, strings.Join(questions, "\n"), strings.Join(vocabulary, "\n"), strings.Join(grammar, "\n"))
	rich := map[string]any{"blocks": []any{
		map[string]any{"type": "heading", "text": "🗣 Тема дня", "size": 2},
		map[string]any{"type": "paragraph", "text": topic.Title + "\n\n" + topic.Introduction},
		map[string]any{"type": "heading", "text": "💬 Вопросы на обсуждение", "size": 2},
		map[string]any{"type": "paragraph", "text": strings.Join(questions, "\n")},
		map[string]any{"type": "details", "summary": "📚 Полезная лексика", "blocks": []any{map[string]any{"type": "paragraph", "text": strings.Join(vocabulary, "\n")}}, "is_open": false},
		map[string]any{"type": "details", "summary": "🧩 Полезная грамматика", "blocks": []any{map[string]any{"type": "paragraph", "text": strings.Join(grammar, "\n\n")}}, "is_open": false},
	}}
	return text, rich
}

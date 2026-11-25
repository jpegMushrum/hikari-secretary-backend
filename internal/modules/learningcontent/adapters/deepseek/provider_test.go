package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
)

func TestDeepSeekProviderRetriesTruncatedResponseAndKeepsRequestedLevel(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		messages := request["messages"].([]any)
		system := messages[0].(map[string]any)["content"].(string)
		if !strings.Contains(system, "Название темы (title) пиши по-японски") {
			t.Error("prompt missing Japanese topic title requirement")
		}
		user := messages[1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "既出語") || !strings.Contains(user, "昔の文法") || !strings.Contains(user, "N3") {
			t.Errorf("history or level missing from prompt: %s", user)
		}
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "length", "message": map[string]any{"content": ""}}}})
			return
		}
		raw, _ := json.Marshal(validTopic())
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(raw)}}}})
	}))
	defer server.Close()
	history := make([]content.Topic, 16)
	history[15] = validTopic()
	history[15].Vocabulary[0].Japanese = "既出語"
	history[15].Grammar[0].Pattern = "昔の文法"
	provider := NewProvider("key", "model", server.URL, time.Second, 5, 10, 3)
	topic, err := provider.Generate(context.Background(), history, content.Options{JLPTLevel: "N3"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || topic.JLPTLevel != "N3" {
		t.Fatalf("calls=%d topic=%+v", calls, topic)
	}
}

func TestDeepSeekRetriesInvalidJSONWithStop(t *testing.T) {
	for _, tc := range []struct {
		name, first string
		recover     bool
	}{
		{"empty", "", true},
		{"spaces", "   ", true},
		{"truncated", `{"title":`, true},
		{"both responses empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				content := tc.first
				if calls > 1 && tc.recover {
					raw, _ := json.Marshal(validTopic())
					content = string(raw)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"finish_reason": "stop", "message": map[string]string{"content": content},
				}}})
			}))
			defer server.Close()
			provider := NewProvider("key", "model", server.URL, time.Second, 5, 10, 3)
			topic, err := provider.Generate(context.Background(), nil, content.Options{JLPTLevel: "N3"})
			if calls != 2 || (err == nil) != tc.recover || (tc.recover && topic.Title == "") {
				t.Fatalf("calls=%d topic=%q err=%v", calls, topic.Title, err)
			}
		})
	}
}

func validTopic() content.Topic {
	questions := []string{"1", "2", "3", "4", "5"}
	vocabulary := make([]content.VocabularyItem, 10)
	for i := range vocabulary {
		vocabulary[i] = content.VocabularyItem{Japanese: "語", Reading: "ご", Translation: "слово"}
	}
	grammar := make([]content.GrammarPoint, 3)
	for i := range grammar {
		grammar[i] = content.GrammarPoint{Pattern: "〜ため", Explanation: "причина", Example: "雨のため、行きません。", Translation: "Не иду из-за дождя."}
	}
	return content.Topic{JLPTLevel: "N3", Title: "Тема", VocabularyTheme: "Лексика", Introduction: "Введение", Questions: questions, Vocabulary: vocabulary, Grammar: grammar}
}

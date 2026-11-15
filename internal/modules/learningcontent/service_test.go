package learningcontent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type providerStub struct {
	topic   Topic
	options Options
	history []Topic
}

func (p *providerStub) Generate(_ context.Context, history []Topic, options Options) (Topic, error) {
	p.history = history
	p.options = options
	return p.topic, nil
}

type repositoryStub struct {
	preferences Options
	history     []Topic
}

func (r *repositoryStub) Preferences(context.Context) (Options, error)     { return r.preferences, nil }
func (r *repositoryStub) History(context.Context, *int64) ([]Topic, error) { return r.history, nil }
func (r *repositoryStub) RecordManual(context.Context, int64, Topic) error { return nil }
func (r *repositoryStub) ReplaceHistory(context.Context, int64, Topic) (bool, error) {
	return true, nil
}

func TestGenerateUsesStoredPreferencesAndRejectsRepeatedTopic(t *testing.T) {
	old := validTopic()
	old.Title = "Повтор"
	repo := &repositoryStub{preferences: Options{JLPTLevel: "N3"}, history: []Topic{old}}
	provider := &providerStub{topic: validTopic()}
	provider.topic.Title = "  повтор  "
	service := NewService(provider, repo, nil, domain.Target{Key: "target", Name: "Target", Destination: "-100"})
	if _, err := service.Generate(context.Background(), nil, nil); err == nil {
		t.Fatal("expected repeated topic error")
	}
	if provider.options.JLPTLevel != "N3" {
		t.Fatalf("provider level=%q", provider.options.JLPTLevel)
	}
}

func TestTopicValidationRequiresConfiguredCounts(t *testing.T) {
	topic := validTopic()
	topic.Questions = topic.Questions[:4]
	if err := topic.Validate(5, 10, 3); err == nil {
		t.Fatal("expected count validation error")
	}
}

func TestDeepSeekProviderRetriesTruncatedResponseAndKeepsRequestedLevel(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		messages := request["messages"].([]any)
		user := messages[1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "既出語") || !strings.Contains(user, "N3") {
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
	history := validTopic()
	history.Vocabulary[0].Japanese = "既出語"
	provider := NewDeepSeekProvider("key", "model", server.URL, time.Second, 5, 10, 3)
	topic, err := provider.Generate(context.Background(), []Topic{history}, Options{JLPTLevel: "N3"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || topic.JLPTLevel != "N3" {
		t.Fatalf("calls=%d topic=%+v", calls, topic)
	}
}

func validTopic() Topic {
	questions := []string{"1", "2", "3", "4", "5"}
	vocabulary := make([]VocabularyItem, 10)
	for i := range vocabulary {
		vocabulary[i] = VocabularyItem{"語", "ご", "слово"}
	}
	grammar := make([]GrammarPoint, 3)
	for i := range grammar {
		grammar[i] = GrammarPoint{"〜ため", "причина", "雨のため、行きません。", "Не иду из-за дождя."}
	}
	return Topic{JLPTLevel: "N3", Title: "Тема", VocabularyTheme: "Лексика", Introduction: "Введение", Questions: questions, Vocabulary: vocabulary, Grammar: grammar}
}

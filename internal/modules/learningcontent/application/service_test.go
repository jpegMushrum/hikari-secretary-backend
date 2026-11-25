package application

import (
	"context"
	"testing"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type providerStub struct {
	topic   content.Topic
	options content.Options
	history []content.Topic
}

func (p *providerStub) Generate(_ context.Context, history []content.Topic, options content.Options) (content.Topic, error) {
	p.history = history
	p.options = options
	return p.topic, nil
}

type repositoryStub struct {
	preferences content.Options
	history     []content.Topic
}

func (r *repositoryStub) Preferences(context.Context) (content.Options, error) {
	return r.preferences, nil
}
func (r *repositoryStub) History(context.Context) ([]content.Topic, error) { return r.history, nil }
func (r *repositoryStub) ScheduleManual(context.Context, domain.ScheduledDraft, content.Topic) (int64, error) {
	return 0, nil
}
func (r *repositoryStub) ReplaceQueuedTopic(context.Context, int64, content.Topic) (bool, error) {
	return true, nil
}

func TestGenerateUsesStoredPreferencesAndRejectsRepeatedTopic(t *testing.T) {
	old := content.Topic{Title: "Повтор"}
	repo := &repositoryStub{preferences: content.Options{JLPTLevel: "N3"}, history: []content.Topic{old}}
	provider := &providerStub{}
	provider.topic.Title = "  повтор  "
	service := NewService(provider, repo, domain.Target{Key: "target", Name: "Target", Destination: "-100"})
	if _, err := service.Generate(context.Background(), nil); err == nil {
		t.Fatal("expected repeated topic error")
	}
	if provider.options.JLPTLevel != "N3" {
		t.Fatalf("provider level=%q", provider.options.JLPTLevel)
	}
}

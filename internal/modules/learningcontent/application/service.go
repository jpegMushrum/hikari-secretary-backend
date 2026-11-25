package application

import (
	"context"
	"time"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type Provider interface {
	Generate(context.Context, []content.Topic, content.Options) (content.Topic, error)
}

type Repository interface {
	Preferences(context.Context) (content.Options, error)
	History(context.Context) ([]content.Topic, error)
	ScheduleManual(context.Context, domain.ScheduledDraft, content.Topic) (int64, error)
	ReplaceQueuedTopic(context.Context, int64, content.Topic) (bool, error)
}

type Service struct {
	provider        Provider
	repository      Repository
	target          domain.Target
	questionCount   int
	vocabularyCount int
	grammarCount    int
}

func NewService(provider Provider, repository Repository, target domain.Target, counts ...int) *Service {
	questions, vocabulary, grammar := 5, 10, 3
	if len(counts) == 3 {
		questions, vocabulary, grammar = counts[0], counts[1], counts[2]
	}
	return &Service{provider: provider, repository: repository, target: target, questionCount: questions, vocabularyCount: vocabulary, grammarCount: grammar}
}

func (s *Service) Generate(ctx context.Context, override *content.Options) (content.Topic, error) {
	options, err := s.repository.Preferences(ctx)
	if err != nil {
		return content.Topic{}, err
	}
	if override != nil {
		options = *override
	}
	history, err := s.repository.History(ctx)
	if err != nil {
		return content.Topic{}, err
	}
	topic, err := s.provider.Generate(ctx, history, options)
	if err != nil {
		return content.Topic{}, err
	}
	for _, old := range history {
		if err := topic.CheckDuplicate(old.Title, old.VocabularyTheme); err != nil {
			return content.Topic{}, err
		}
	}
	return topic, nil
}

func (s *Service) Schedule(ctx context.Context, topic content.Topic, creator int64, scheduledAt time.Time, sourceKey string) (int64, error) {
	draft, err := s.scheduledDraft(topic, creator, scheduledAt, sourceKey)
	if err != nil {
		return 0, err
	}
	if err := publishing.PrepareScheduledDraft(&draft); err != nil {
		return 0, err
	}
	return s.repository.ScheduleManual(ctx, draft, topic)
}

func (s *Service) scheduledDraft(topic content.Topic, creator int64, when time.Time, key string) (domain.ScheduledDraft, error) {
	if err := topic.Validate(s.questionCount, s.vocabularyCount, s.grammarCount); err != nil {
		return domain.ScheduledDraft{}, err
	}
	text, rich := Render(topic)
	return domain.ScheduledDraft{Draft: domain.Draft{CreatorTelegramID: creator, Text: text, RichMessage: rich, Source: "ai_daily_topic", SourceKey: key}, Targets: []domain.Target{s.target}, ScheduledAt: when}, nil
}

func (s *Service) Replace(ctx context.Context, publicationID int64, topic content.Topic) (bool, error) {
	if err := topic.Validate(s.questionCount, s.vocabularyCount, s.grammarCount); err != nil {
		return false, err
	}
	return s.repository.ReplaceQueuedTopic(ctx, publicationID, topic)
}

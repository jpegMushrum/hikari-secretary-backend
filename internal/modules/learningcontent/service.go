package learningcontent

import (
	"context"
	"fmt"
	"time"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type Repository interface {
	Preferences(context.Context) (Options, error)
	History(context.Context, *int64) ([]Topic, error)
	RecordManual(context.Context, int64, Topic) error
	ReplaceHistory(context.Context, int64, Topic) (bool, error)
}

type Service struct {
	provider        Provider
	repository      Repository
	publications    *application.Service
	target          domain.Target
	questionCount   int
	vocabularyCount int
	grammarCount    int
}

func NewService(provider Provider, repository Repository, publications *application.Service, target domain.Target, counts ...int) *Service {
	questions, vocabulary, grammar := 5, 10, 3
	if len(counts) == 3 {
		questions, vocabulary, grammar = counts[0], counts[1], counts[2]
	}
	return &Service{provider: provider, repository: repository, publications: publications, target: target, questionCount: questions, vocabularyCount: vocabulary, grammarCount: grammar}
}

func (s *Service) Generate(ctx context.Context, override *Options, excludePublicationID *int64) (Topic, error) {
	options, err := s.repository.Preferences(ctx)
	if err != nil {
		return Topic{}, err
	}
	if override != nil {
		options = *override
	}
	history, err := s.repository.History(ctx, excludePublicationID)
	if err != nil {
		return Topic{}, err
	}
	topic, err := s.provider.Generate(ctx, history, options)
	if err != nil {
		return Topic{}, err
	}
	for _, old := range history {
		if normalize(old.Title) == normalize(topic.Title) {
			return Topic{}, fmt.Errorf("model repeated a conversation topic")
		}
		if normalize(old.VocabularyTheme) == normalize(topic.VocabularyTheme) {
			return Topic{}, fmt.Errorf("model repeated a vocabulary theme")
		}
	}
	return topic, nil
}

func (s *Service) Schedule(ctx context.Context, topic Topic, creator int64, scheduledAt time.Time, sourceKey string, recordHistory bool) (int64, error) {
	if err := topic.Validate(s.questionCount, s.vocabularyCount, s.grammarCount); err != nil {
		return 0, err
	}
	text, rich := Render(topic)
	id, err := s.publications.CreateScheduled(ctx, domain.ScheduledDraft{Draft: domain.Draft{CreatorTelegramID: creator, Text: text, RichMessage: rich, Source: "ai_daily_topic", SourceKey: sourceKey}, Targets: []domain.Target{s.target}, ScheduledAt: scheduledAt})
	if err != nil {
		return 0, err
	}
	if recordHistory {
		if err = s.repository.RecordManual(ctx, id, topic); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (s *Service) Replace(ctx context.Context, publicationID int64, topic Topic) (bool, error) {
	if err := topic.Validate(s.questionCount, s.vocabularyCount, s.grammarCount); err != nil {
		return false, err
	}
	text, rich := Render(topic)
	ok, err := s.publications.ReplaceScheduled(ctx, publicationID, text, rich)
	if err != nil || !ok {
		return ok, err
	}
	return s.repository.ReplaceHistory(ctx, publicationID, topic)
}

package application

import "context"

type Flow struct {
	TelegramUserID int64
	Kind           string
	PublicationID  *int64
	Stage          *string
}
type Repository interface {
	Get(context.Context, int64, string) (Flow, bool, error)
	Put(context.Context, Flow) error
	Delete(context.Context, Flow) (bool, error)
}
type Service struct{ repository Repository }

func NewService(repository Repository) *Service { return &Service{repository: repository} }
func ValidKind(kind string) bool {
	return kind == "registration" || kind == "profile_edit" || kind == "feedback"
}
func (s *Service) Get(ctx context.Context, userID int64, kind string) (Flow, bool, error) {
	return s.repository.Get(ctx, userID, kind)
}
func (s *Service) Put(ctx context.Context, flow Flow) error { return s.repository.Put(ctx, flow) }
func (s *Service) Delete(ctx context.Context, match Flow) (bool, error) {
	return s.repository.Delete(ctx, match)
}

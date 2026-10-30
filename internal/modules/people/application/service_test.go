package application

import (
	"context"
	"testing"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
)

type fakeRepository struct {
	command domain.SyncIdentityCommand
}

func (r *fakeRepository) SyncTelegramIdentity(_ context.Context, command domain.SyncIdentityCommand) (domain.TelegramIdentity, error) {
	r.command = command
	return domain.TelegramIdentity{PersonID: 1, TelegramUserID: command.TelegramUserID, Username: command.Username, DisplayName: command.DisplayName}, nil
}

func (*fakeRepository) ListPeople(context.Context, ListPeopleQuery) (ListPeopleResult, error) {
	return ListPeopleResult{}, nil
}

func TestSyncTelegramIdentityNormalizesInput(t *testing.T) {
	repository := &fakeRepository{}
	service := NewService(repository)

	_, err := service.SyncTelegramIdentity(context.Background(), domain.SyncIdentityCommand{
		TelegramUserID: 123,
		Username:       " @hikari ",
		DisplayName:    "  Hikari User  ",
	})
	if err != nil {
		t.Fatalf("SyncTelegramIdentity() error = %v", err)
	}
	if repository.command.Username != "hikari" || repository.command.DisplayName != "Hikari User" {
		t.Errorf("normalized command = %#v", repository.command)
	}
}

func TestSyncTelegramIdentityRejectsMissingName(t *testing.T) {
	service := NewService(&fakeRepository{})
	_, err := service.SyncTelegramIdentity(context.Background(), domain.SyncIdentityCommand{TelegramUserID: 123})
	if err != domain.ErrInvalidDisplayName {
		t.Fatalf("SyncTelegramIdentity() error = %v, want ErrInvalidDisplayName", err)
	}
}

func TestListPeopleKeepsQueryUntouched(t *testing.T) {
	service := NewService(&fakeRepository{})
	query := ListPeopleQuery{Page: pagination.Page{Number: 1, Size: 10}, Sort: SortByName, Order: SortAscending}
	if _, err := service.ListPeople(context.Background(), query); err != nil {
		t.Fatalf("ListPeople() error = %v", err)
	}
}

package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidTelegramUserID = errors.New("Telegram user ID must be positive")
	ErrInvalidDisplayName    = errors.New("display name must contain 1 to 256 characters")
	ErrInvalidUsername       = errors.New("username must contain at most 32 characters")
)

type SyncIdentityCommand struct {
	TelegramUserID int64
	Username       string
	DisplayName    string
}

type TelegramIdentity struct {
	PersonID       int64
	TelegramUserID int64
	Username       string
	DisplayName    string
}

func NormalizeSyncIdentity(command SyncIdentityCommand) (SyncIdentityCommand, error) {
	if command.TelegramUserID <= 0 {
		return SyncIdentityCommand{}, ErrInvalidTelegramUserID
	}

	command.DisplayName = strings.TrimSpace(command.DisplayName)
	if length := utf8.RuneCountInString(command.DisplayName); length == 0 || length > 256 {
		return SyncIdentityCommand{}, ErrInvalidDisplayName
	}

	command.Username = strings.TrimPrefix(strings.TrimSpace(command.Username), "@")
	if utf8.RuneCountInString(command.Username) > 32 {
		return SyncIdentityCommand{}, ErrInvalidUsername
	}

	return command, nil
}

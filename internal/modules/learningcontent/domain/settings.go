package domain

import (
	"errors"
	"unicode/utf8"
)

var ErrInvalidSettings = errors.New("invalid AI settings")

// Settings is shared by the bot and administrator API. Provider credentials
// remain deployment configuration and are never part of this model.
type Settings struct {
	JLPTLevel    string  `json:"jlpt_level"`
	AdminComment *string `json:"admin_comment"`
}

func (s Settings) Validate() error {
	switch s.JLPTLevel {
	case "N1", "N2", "N3", "N4", "N5":
	default:
		return ErrInvalidSettings
	}
	if s.AdminComment != nil && utf8.RuneCountInString(*s.AdminComment) > 500 {
		return ErrInvalidSettings
	}
	return nil
}

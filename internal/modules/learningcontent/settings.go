package learningcontent

import (
	"context"
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

func (r *PostgresRepository) Settings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `SELECT jlpt_level,admin_comment FROM ai_topic_settings WHERE id=1`).Scan(&s.JLPTLevel, &s.AdminComment)
	return s, err
}

func (r *PostgresRepository) SaveSettings(ctx context.Context, s Settings, actor int64) error {
	if err := s.Validate(); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_settings SET jlpt_level=$1,admin_comment=$2,updated_by=$3,updated_at=NOW() WHERE id=1`, s.JLPTLevel, s.AdminComment, actor)
	return err
}

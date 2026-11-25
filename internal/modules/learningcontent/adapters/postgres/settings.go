package postgres

import (
	"context"

	content "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent/domain"
)

func (r *Repository) Settings(ctx context.Context) (content.Settings, error) {
	var s content.Settings
	err := r.pool.QueryRow(ctx, `SELECT jlpt_level,admin_comment FROM ai_topic_settings WHERE id=1`).Scan(&s.JLPTLevel, &s.AdminComment)
	return s, err
}

func (r *Repository) SaveSettings(ctx context.Context, s content.Settings, actor int64) error {
	if err := s.Validate(); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `UPDATE ai_topic_settings SET jlpt_level=$1,admin_comment=$2,updated_by=$3,updated_at=NOW() WHERE id=1`, s.JLPTLevel, s.AdminComment, actor)
	return err
}

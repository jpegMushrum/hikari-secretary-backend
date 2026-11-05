package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) CreateDraft(ctx context.Context, draft domain.Draft) (int64, error) {
	entitiesJSON, err := json.Marshal(draft.Entities)
	if err != nil {
		return 0, err
	}
	var rich any
	if draft.RichMessage != nil {
		richJSON, marshalErr := json.Marshal(draft.RichMessage)
		if marshalErr != nil {
			return 0, marshalErr
		}
		rich = string(richJSON)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO publications(creator_telegram_id,text,entities,rich_message,source,source_key) VALUES($1,$2,$3::jsonb,$4::jsonb,$5,NULLIF($6,'')) ON CONFLICT(source_key) DO UPDATE SET source_key=EXCLUDED.source_key RETURNING id`, draft.CreatorTelegramID, draft.Text, string(entitiesJSON), rich, draft.Source, draft.SourceKey).Scan(&id)
	if err != nil {
		return 0, err
	}
	for _, media := range draft.Media {
		if _, err = tx.Exec(ctx, `INSERT INTO publication_media(publication_id,position,path) VALUES($1,$2,$3) ON CONFLICT(publication_id,position) DO NOTHING`, id, media.Position, media.Path); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

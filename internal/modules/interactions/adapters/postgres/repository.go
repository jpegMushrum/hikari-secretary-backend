package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/application"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }
func (r *Repository) Get(ctx context.Context, userID int64, kind string) (application.Flow, bool, error) {
	var flow application.Flow
	err := r.pool.QueryRow(ctx, `SELECT telegram_user_id,kind,publication_id,stage FROM bot_flows WHERE telegram_user_id=$1 AND kind=$2`, userID, kind).Scan(&flow.TelegramUserID, &flow.Kind, &flow.PublicationID, &flow.Stage)
	if err == pgx.ErrNoRows {
		return application.Flow{}, false, nil
	}
	return flow, err == nil, err
}
func (r *Repository) Put(ctx context.Context, flow application.Flow) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO bot_flows(telegram_user_id,kind,publication_id,stage,updated_at) VALUES($1,$2,$3,$4,NOW()) ON CONFLICT(telegram_user_id,kind) DO UPDATE SET publication_id=EXCLUDED.publication_id,stage=EXCLUDED.stage,updated_at=NOW()`, flow.TelegramUserID, flow.Kind, flow.PublicationID, flow.Stage)
	return err
}
func (r *Repository) Delete(ctx context.Context, userID int64, kind string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM bot_flows WHERE telegram_user_id=$1 AND kind=$2`, userID, kind)
	return err
}

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/serviceauth"
)

type reminderScheduler interface {
	EnqueueReminders(context.Context) error
}

func runSantaReminderScheduler(ctx context.Context, service reminderScheduler, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		run, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := service.EnqueueReminders(run)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Error("schedule Santa reminders", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Only mounted for the local gateway with its synthetic Telegram token.
func localSantaInbox(pool *pgxpool.Pool, token string) http.HandlerFunc {
	auth := serviceauth.NewValidator(token)
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.ValidateAuthorization(r.Header.Get("Authorization")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user := r.PathValue("telegram_id")
		if _, err := strconv.ParseInt(user, 10, 64); err != nil {
			http.Error(w, "invalid recipient", http.StatusBadRequest)
			return
		}
		rows, err := pool.Query(r.Context(), `SELECT id, payload->>'text', payload->>'path', payload->>'profile_url' FROM outbox
            WHERE topic='santa.notice' AND payload->>'telegram_user_id'=$1 AND available_at<=NOW()
            AND aggregate_id IN (SELECT id FROM santa_seasons WHERE status<>'closed')
            AND (`+santa.NoticeDispatchableSQL+`) ORDER BY id DESC LIMIT 30`, user)
		if err != nil {
			http.Error(w, "inbox unavailable", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type notice struct {
			ID         int64   `json:"id"`
			Text       string  `json:"text"`
			Path       string  `json:"path"`
			ProfileURL *string `json:"profile_url,omitempty"`
		}
		items := []notice{}
		for rows.Next() {
			var n notice
			if err := rows.Scan(&n.ID, &n.Text, &n.Path, &n.ProfileURL); err != nil {
				http.Error(w, "inbox unavailable", http.StatusInternalServerError)
				return
			}
			items = append(items, n)
		}
		if rows.Err() != nil {
			http.Error(w, "inbox unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(items)
	}
}

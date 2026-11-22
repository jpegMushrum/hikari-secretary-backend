package memberapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	peoplepg "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/adapters/postgres"
	peopleapp "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
)

type Handler struct {
	pool      *pgxpool.Pool
	reminders []int
}
type personKey struct{}

func personID(r *http.Request) int64 { return r.Context().Value(personKey{}).(int64) }

func NewRouter(pool *pgxpool.Pool, reminderOptions ...int) http.Handler {
	h := &Handler{pool: pool, reminders: reminderOptions}
	if len(h.reminders) == 0 {
		h.reminders = []int{30, 60, 120, 360, 1440}
	}
	mux := http.NewServeMux()
	const base = "/api/v1/member"
	mux.HandleFunc("GET "+base+"/me", func(w http.ResponseWriter, r *http.Request) {
		identity, _ := requestcontext.Member(r.Context())
		i := identity.Telegram
		writeJSON(w, 200, map[string]any{"telegram_id": i.TelegramID, "first_name": i.FirstName, "last_name": i.LastName, "username": i.Username, "is_admin": identity.IsAdmin})
	})
	mux.HandleFunc("GET "+base+"/profile", h.profile)
	mux.HandleFunc("PUT "+base+"/profile", h.updateProfile)
	mux.HandleFunc("PATCH "+base+"/profile", h.updatePreferences)
	mux.HandleFunc("GET "+base+"/events", h.events)
	mux.HandleFunc("GET "+base+"/events/{id}", h.event)
	mux.HandleFunc("GET "+base+"/events/{id}/media/{position}", h.media)
	mux.HandleFunc("POST "+base+"/events/{id}/registration", h.register)
	mux.HandleFunc("PATCH "+base+"/events/{id}/registration", h.updateReminder)
	mux.HandleFunc("DELETE "+base+"/events/{id}/registration", h.cancelRegistration)
	mux.HandleFunc("PUT "+base+"/events/{id}/attendance", h.attendance)
	mux.HandleFunc("GET "+base+"/feedback", h.feedback)
	mux.HandleFunc("POST "+base+"/feedback", h.createFeedback)
	mux.HandleFunc("DELETE "+base+"/feedback/{id}", h.deleteFeedback)
	people := peopleapp.NewService(peoplepg.NewRepository(pool))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, ok := requestcontext.Member(r.Context())
		if !ok {
			writeError(w, 401, "unauthorized")
			return
		}
		name := strings.TrimSpace(i.Telegram.FirstName + " " + i.Telegram.LastName)
		if name == "" {
			name = strconv.FormatInt(i.Telegram.TelegramID, 10)
		}
		// A returning user only needs a read. Sync newly observed or changed
		// identities using the same idempotent transaction as the bot.
		var id int64
		err := pool.QueryRow(r.Context(), "SELECT person_id FROM telegram_identities WHERE telegram_user_id=$1 AND COALESCE(username,'')=$2 AND display_name=$3", i.Telegram.TelegramID, i.Telegram.Username, name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			var synced domain.TelegramIdentity
			synced, err = people.SyncTelegramIdentity(r.Context(), domain.SyncIdentityCommand{TelegramUserID: i.Telegram.TelegramID, Username: i.Telegram.Username, DisplayName: name})
			id = synced.PersonID
		}
		if err != nil {
			writeError(w, 500, "internal_error")
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), personKey{}, id)))
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func dbError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	writeError(w, 500, "internal_error")
}
func pathID(r *http.Request) (int64, bool) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, e == nil && id > 0
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid_request")
		return false
	}
	return true
}

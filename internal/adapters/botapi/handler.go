// Package botapi contains the private HTTP adapter used by the Python bot.
// It is intentionally separate from the public Mini App adapter: the bot is
// authenticated with BOT_SERVICE_TOKEN and is never treated as an admin user.
package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	interactions "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/interactions/application"
	learningcontent "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/learningcontent"
	publishing "github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/publishing/domain"
)

const (
	maxActiveEvents = 5
	maxPageSize     = 100
)

type Handler struct {
	pool         *pgxpool.Pool
	flows        *interactions.Service
	publications *publishing.Service
	ai           *learningcontent.Service
}

func NewRouter(pool *pgxpool.Pool, flows *interactions.Service, publications *publishing.Service, aiServices ...*learningcontent.Service) http.Handler {
	var ai *learningcontent.Service
	if len(aiServices) > 0 {
		ai = aiServices[0]
	}
	h := &Handler{pool: pool, flows: flows, publications: publications, ai: ai}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /people/profile", h.upsertProfile)
	mux.HandleFunc("GET /people/{telegram_id}", h.getProfile)
	mux.HandleFunc("POST /people/{telegram_id}/feedback/toggle", h.toggleFeedback)
	mux.HandleFunc("GET /people/{telegram_id}/registrations", h.userRegistrations)
	mux.HandleFunc("GET /people/{telegram_id}/feedback", h.feedbackSetting)
	mux.HandleFunc("GET /flows/{telegram_id}/{kind}", h.getFlow)
	mux.HandleFunc("PUT /flows/{telegram_id}/{kind}", h.putFlow)
	mux.HandleFunc("DELETE /flows/{telegram_id}/{kind}", h.deleteFlow)

	mux.HandleFunc("POST /events", h.createEvent)
	mux.HandleFunc("GET /events", h.listEvents)
	mux.HandleFunc("GET /events/{publication_id}", h.getEvent)
	mux.HandleFunc("DELETE /events/{publication_id}", h.deleteEvent)
	mux.HandleFunc("POST /events/{publication_id}/published", h.markPublished)
	mux.HandleFunc("POST /events/{publication_id}/cancel", h.cancelEvent)
	mux.HandleFunc("POST /events/{publication_id}/registrations", h.register)
	mux.HandleFunc("GET /events/{publication_id}/registrations/{telegram_id}", h.registrationStatus)
	mux.HandleFunc("DELETE /events/{publication_id}/registrations/{telegram_id}", h.cancelRegistration)
	mux.HandleFunc("GET /events/{publication_id}/registrations", h.eventRegistrations)

	mux.HandleFunc("GET /reminders/due", h.dueReminders)
	mux.HandleFunc("POST /reminders/mark-sent", h.markReminderSent)
	mux.HandleFunc("GET /attendance-prompts/due", h.dueAttendancePrompts)
	mux.HandleFunc("POST /attendance-prompts/mark-sent", h.markAttendancePromptSent)
	mux.HandleFunc("POST /attendance", h.recordAttendance)

	mux.HandleFunc("POST /feedback", h.saveFeedback)
	mux.HandleFunc("GET /feedback", h.feedbackPage)
	mux.HandleFunc("GET /feedback/{id}", h.feedbackEntry)
	mux.HandleFunc("GET /statistics/people", h.peopleStatistics)
	mux.HandleFunc("POST /publications", h.createPublication)
	mux.HandleFunc("POST /publications/scheduled", h.createScheduledPublication)
	mux.HandleFunc("GET /publications", h.listPublications)
	mux.HandleFunc("GET /publications/{id}", h.getPublication)
	mux.HandleFunc("POST /publications/{id}/targets/toggle", h.togglePublicationTarget)
	mux.HandleFunc("POST /publications/{id}/schedule", h.schedulePublication)
	mux.HandleFunc("POST /publications/{id}/cancel", h.cancelPublication)
	mux.HandleFunc("POST /deliveries/claim", h.claimDelivery)
	mux.HandleFunc("POST /deliveries/recover", h.recoverDeliveries)
	mux.HandleFunc("POST /deliveries/{id}/succeeded", h.deliverySucceeded)
	mux.HandleFunc("POST /deliveries/{id}/failed", h.deliveryFailed)
	mux.HandleFunc("GET /publication-notifications", h.publicationNotices)
	mux.HandleFunc("POST /publication-notifications/{id}/sent", h.markPublicationNotified)
	mux.HandleFunc("GET /ai-topics/preferences", h.aiPreferences)
	mux.HandleFunc("PUT /ai-topics/preferences", h.saveAIPreferences)
	mux.HandleFunc("GET /ai-topics/capabilities", h.aiCapabilities)
	mux.HandleFunc("POST /ai-topics/generate", h.generateAITopic)
	mux.HandleFunc("POST /ai-topics/publications", h.publishAITopic)
	mux.HandleFunc("PUT /ai-topics/publications/{publication_id}", h.updateAITopicPublication)
	mux.HandleFunc("GET /ai-topics/failure-notifications", h.aiFailureNotices)
	mux.HandleFunc("POST /ai-topics/failure-notifications/{id}/sent", h.markAIFailureNotified)
	return http.StripPrefix("/internal/bot/v1", mux)
}

func (h *Handler) aiFailureNotices(w http.ResponseWriter, r *http.Request) {
	items, err := learningcontent.NewPostgresRepository(h.pool).FailureNotices(r.Context())
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, items)
}
func (h *Handler) markAIFailureNotified(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := learningcontent.NewPostgresRepository(h.pool).MarkFailureNotified(r.Context(), id); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handler) aiCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"enabled": h.ai != nil})
}
func (h *Handler) generateAITopic(w http.ResponseWriter, r *http.Request) {
	if h.ai == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_not_configured")
		return
	}
	var body struct {
		Options              *learningcontent.Options `json:"options"`
		ExcludePublicationID *int64                   `json:"exclude_publication_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	topic, err := h.ai.Generate(r.Context(), body.Options, body.ExcludePublicationID)
	if err != nil {
		log.Printf("AI generation failed: %v", err)
		writeError(w, http.StatusBadGateway, "ai_generation_failed")
		return
	}
	writeJSON(w, 200, topic)
}
func (h *Handler) publishAITopic(w http.ResponseWriter, r *http.Request) {
	if h.ai == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_not_configured")
		return
	}
	var body struct {
		Topic       learningcontent.Topic `json:"topic"`
		CreatorID   int64                 `json:"creator_id"`
		ScheduledAt string                `json:"scheduled_at"`
		SourceKey   string                `json:"source_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	scheduled, err := time.Parse(time.RFC3339, body.ScheduledAt)
	if err != nil || body.CreatorID <= 0 {
		writeError(w, 400, "invalid_request")
		return
	}
	id, err := h.ai.Schedule(r.Context(), body.Topic, body.CreatorID, scheduled, body.SourceKey, true)
	if errors.Is(err, domain.ErrPendingPublicationLimit) {
		writeError(w, 409, "capacity_limit")
		return
	}
	if err != nil {
		log.Printf("AI publication failed: %v", err)
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}
func (h *Handler) updateAITopicPublication(w http.ResponseWriter, r *http.Request) {
	if h.ai == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_not_configured")
		return
	}
	id, ok := pathInt64(r, "publication_id")
	var body struct {
		Topic learningcontent.Topic `json:"topic"`
	}
	if !ok || !decodeJSON(w, r, &body) {
		writeError(w, 400, "invalid_request")
		return
	}
	replaced, err := h.ai.Replace(r.Context(), id, body.Topic)
	if err != nil {
		log.Printf("AI publication replacement failed: %v", err)
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"replaced": replaced})
}

type profileRequest struct {
	TelegramUserID       int64   `json:"telegram_user_id"`
	Username             string  `json:"username"`
	DisplayName          string  `json:"display_name"`
	FullName             *string `json:"full_name"`
	IsRussianCitizen     *bool   `json:"is_russian_citizen"`
	IsITMOStudent        *bool   `json:"is_itmo_student"`
	FeedbackOffersEnable *bool   `json:"feedback_offers_enabled"`
}

func (h *Handler) upsertProfile(writer http.ResponseWriter, request *http.Request) {
	var body profileRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	displayName := strings.TrimSpace(body.DisplayName)
	if body.TelegramUserID <= 0 || utf8.RuneCountInString(displayName) == 0 || utf8.RuneCountInString(displayName) > 256 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	username := strings.TrimPrefix(strings.TrimSpace(body.Username), "@")
	if utf8.RuneCountInString(username) > 32 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}

	ctx := request.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	personID, err := ensureIdentity(ctx, tx, body.TelegramUserID, username, displayName)
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE people
			SET full_name = CASE WHEN $2::text IS NULL THEN full_name ELSE NULLIF(BTRIM($2), '') END,
				is_russian_citizen = COALESCE($3, is_russian_citizen),
				is_itmo_student = COALESCE($4, is_itmo_student),
				feedback_offers_enabled = COALESCE($5, feedback_offers_enabled),
				updated_at = NOW()
			WHERE id = $1`, personID, body.FullName, body.IsRussianCitizen, body.IsITMOStudent, body.FeedbackOffersEnable)
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	profile, err := h.profile(ctx, body.TelegramUserID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, profile)
}

func (h *Handler) getProfile(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	profile, err := h.profile(request.Context(), telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, profile)
}

func (h *Handler) profile(ctx context.Context, telegramID int64) (map[string]any, error) {
	var (
		personID, userID                int64
		fullName, username, displayName *string
		citizen, itmo                   *bool
		feedbackEnabled                 bool
	)
	err := h.pool.QueryRow(ctx, `
		SELECT p.id, ti.telegram_user_id, p.full_name, ti.username, ti.display_name,
			p.is_russian_citizen, p.is_itmo_student, p.feedback_offers_enabled
		FROM people p JOIN telegram_identities ti ON ti.person_id=p.id
		WHERE ti.telegram_user_id=$1`, telegramID).Scan(
		&personID, &userID, &fullName, &username, &displayName, &citizen, &itmo, &feedbackEnabled,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"person_id":               personID,
		"telegram_user_id":        userID,
		"full_name":               fullName,
		"telegram_username":       username,
		"telegram_first_name":     displayName,
		"telegram_last_name":      "",
		"is_russian_citizen":      citizen,
		"is_itmo_student":         itmo,
		"feedback_enabled":        feedbackEnabled,
		"feedback_offers_enabled": feedbackEnabled,
	}, nil
}

func (h *Handler) feedbackSetting(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	profile, err := h.profile(request.Context(), telegramID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"feedback_enabled": profile["feedback_enabled"]})
}

func (h *Handler) getFlow(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	kind := request.PathValue("kind")
	if !ok || !interactions.ValidKind(kind) {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	flow, found, err := h.flows.Get(request.Context(), telegramID, kind)
	if !found && err == nil {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"user_id": flow.TelegramUserID, "post_id": flow.PublicationID, "stage": flow.Stage})
}

func (h *Handler) putFlow(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	kind := request.PathValue("kind")
	var body struct {
		PublicationID *int64  `json:"publication_id"`
		Stage         *string `json:"stage"`
	}
	if !ok || !interactions.ValidKind(kind) || !decodeJSON(writer, request, &body) || (body.PublicationID != nil && *body.PublicationID <= 0) {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if body.Stage != nil && len(strings.TrimSpace(*body.Stage)) > 64 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	err := h.flows.Put(request.Context(), interactions.Flow{TelegramUserID: telegramID, Kind: kind, PublicationID: body.PublicationID, Stage: body.Stage})
	if err != nil {
		if strings.Contains(err.Error(), "foreign key") {
			writeError(writer, http.StatusNotFound, "not_found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"stored": true})
}

func (h *Handler) deleteFlow(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	kind := request.PathValue("kind")
	if !ok || !interactions.ValidKind(kind) {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	err := h.flows.Delete(request.Context(), telegramID, kind)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"deleted": true})
}

type publicationRequest struct {
	CreatorID   int64            `json:"creator_id"`
	Text        string           `json:"text"`
	Entities    []map[string]any `json:"entities"`
	MediaPaths  []string         `json:"media_paths"`
	RichMessage map[string]any   `json:"rich_message"`
	Source      string           `json:"source"`
	SourceKey   string           `json:"source_key"`
}

func (h *Handler) createPublication(w http.ResponseWriter, r *http.Request) {
	var body publicationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	id, err := h.publications.CreateDraft(r.Context(), domain.Draft{CreatorTelegramID: body.CreatorID, Text: body.Text, Entities: body.Entities, RichMessage: body.RichMessage, Media: mediaFromPaths(body.MediaPaths), Source: body.Source, SourceKey: body.SourceKey})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type scheduledPublicationRequest struct {
	publicationRequest
	ScheduledAt string `json:"scheduled_at"`
	Targets     []struct {
		Key             string `json:"key"`
		Name            string `json:"name"`
		Destination     string `json:"destination"`
		MessageThreadID *int64 `json:"message_thread_id"`
	} `json:"targets"`
}

func (h *Handler) createScheduledPublication(w http.ResponseWriter, r *http.Request) {
	var body scheduledPublicationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	scheduledAt, err := time.Parse(time.RFC3339, body.ScheduledAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	targets := make([]domain.Target, 0, len(body.Targets))
	for _, target := range body.Targets {
		targets = append(targets, domain.Target{
			Key: target.Key, Name: target.Name, Destination: target.Destination,
			MessageThreadID: target.MessageThreadID,
		})
	}
	id, err := h.publications.CreateScheduled(r.Context(), domain.ScheduledDraft{
		Draft: domain.Draft{
			CreatorTelegramID: body.CreatorID, Text: body.Text, Entities: body.Entities,
			RichMessage: body.RichMessage, Media: mediaFromPaths(body.MediaPaths),
			Source: body.Source, SourceKey: body.SourceKey,
		},
		Targets: targets, ScheduledAt: scheduledAt,
	})
	if err != nil {
		if errors.Is(err, domain.ErrPendingPublicationLimit) {
			writeError(w, http.StatusConflict, "capacity_limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}
func mediaFromPaths(paths []string) []domain.Media {
	result := make([]domain.Media, 0, len(paths))
	for i, path := range paths {
		result = append(result, domain.Media{Position: i, Path: path})
	}
	return result
}
func (h *Handler) getPublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	item, found, err := h.publications.Publication(r.Context(), id)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	if !found {
		writeError(w, 404, "not_found")
		return
	}
	writeJSON(w, 200, item)
}
func (h *Handler) listPublications(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		limit = parsed
	}
	items, err := h.publications.Queue(r.Context(), limit)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, items)
}
func (h *Handler) togglePublicationTarget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	var body struct {
		Key             string `json:"key"`
		Name            string `json:"name"`
		Destination     string `json:"destination"`
		MessageThreadID *int64 `json:"message_thread_id"`
	}
	if !ok || !decodeJSON(w, r, &body) {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := h.publications.ToggleTarget(r.Context(), id, domain.Target{Key: body.Key, Name: body.Name, Destination: body.Destination, MessageThreadID: body.MessageThreadID}); err != nil {
		writeError(w, 409, "invalid_state")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (h *Handler) schedulePublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	var body struct {
		ScheduledAt string `json:"scheduled_at"`
	}
	if !ok || !decodeJSON(w, r, &body) {
		writeError(w, 400, "invalid_request")
		return
	}
	at, err := time.Parse(time.RFC3339, body.ScheduledAt)
	if err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	scheduled, err := h.publications.Schedule(r.Context(), id, at)
	if err != nil {
		if errors.Is(err, domain.ErrPendingPublicationLimit) {
			writeError(w, http.StatusConflict, "capacity_limit")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"scheduled": scheduled})
}
func (h *Handler) cancelPublication(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	cancelled, err := h.publications.Cancel(r.Context(), id)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"cancelled": cancelled})
}
func (h *Handler) claimDelivery(w http.ResponseWriter, r *http.Request) {
	delivery, err := h.publications.ClaimDue(r.Context())
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	if delivery == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, 200, delivery)
}
func (h *Handler) recoverDeliveries(w http.ResponseWriter, r *http.Request) {
	if err := h.publications.RecoverInterruptedDeliveries(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h *Handler) deliverySucceeded(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	var body struct {
		ExternalID string `json:"external_id"`
	}
	if !ok || !decodeJSON(w, r, &body) {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := h.publications.Succeeded(r.Context(), id, body.ExternalID); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (h *Handler) deliveryFailed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	var body struct {
		Reason      string `json:"reason"`
		MaxAttempts int    `json:"max_attempts"`
	}
	if !ok || !decodeJSON(w, r, &body) {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := h.publications.Failed(r.Context(), id, body.Reason, body.MaxAttempts); err != nil {
		writeError(w, 400, "invalid_request")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (h *Handler) publicationNotices(w http.ResponseWriter, r *http.Request) {
	notices, err := h.publications.Notices(r.Context())
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, notices)
}
func (h *Handler) markPublicationNotified(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeError(w, 400, "invalid_request")
		return
	}
	if err := h.publications.MarkNotified(r.Context(), id); err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handler) aiPreferences(w http.ResponseWriter, r *http.Request) {
	var level string
	var comment *string
	err := h.pool.QueryRow(r.Context(), `SELECT jlpt_level,admin_comment FROM ai_topic_settings WHERE id=1`).Scan(&level, &comment)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"jlpt_level": level, "admin_comment": comment})
}
func (h *Handler) saveAIPreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JLPTLevel    string  `json:"jlpt_level"`
		AdminComment *string `json:"admin_comment"`
		UpdatedBy    int64   `json:"updated_by"`
	}
	if !decodeJSON(w, r, &body) || !map[string]bool{"N5": true, "N4": true, "N3": true, "N2": true, "N1": true}[body.JLPTLevel] {
		writeError(w, 400, "invalid_request")
		return
	}
	if body.AdminComment != nil && utf8.RuneCountInString(*body.AdminComment) > 500 {
		writeError(w, 400, "invalid_request")
		return
	}
	_, err := h.pool.Exec(r.Context(), `UPDATE ai_topic_settings SET jlpt_level=$1,admin_comment=$2,updated_by=$3,updated_at=NOW() WHERE id=1`, body.JLPTLevel, body.AdminComment, body.UpdatedBy)
	if err != nil {
		writeError(w, 500, "internal_error")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (h *Handler) toggleFeedback(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var enabled bool
	err := h.pool.QueryRow(request.Context(), `
		UPDATE people p SET feedback_offers_enabled=NOT p.feedback_offers_enabled, updated_at=NOW()
		FROM telegram_identities ti
		WHERE ti.person_id=p.id AND ti.telegram_user_id=$1
		RETURNING p.feedback_offers_enabled`, telegramID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"feedback_enabled": enabled})
}

type eventRequest struct {
	PublicationID     int64  `json:"publication_id"`
	Title             string `json:"title"`
	StartsAt          string `json:"starts_at"`
	EndsAt            string `json:"ends_at"`
	CreatorTelegramID int64  `json:"creator_telegram_id"`
}

func (h *Handler) createEvent(writer http.ResponseWriter, request *http.Request) {
	var body eventRequest
	if !decodeJSON(writer, request, &body) || body.PublicationID <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	start, end, ok := parseEventTimes(body.StartsAt, body.EndsAt)
	if !ok || utf8.RuneCountInString(strings.TrimSpace(body.Title)) == 0 || utf8.RuneCountInString(strings.TrimSpace(body.Title)) > 160 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx := request.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(20261105)); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	var eventID int64
	err = tx.QueryRow(ctx, `SELECT event_id FROM event_publication_references WHERE external_publication_id=$1 ORDER BY CASE provider WHEN 'hikari_publishing' THEN 0 ELSE 1 END LIMIT 1`, strconv.FormatInt(body.PublicationID, 10)).Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		var activeCount int64
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE status='active' AND ends_at>NOW()`).Scan(&activeCount); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		if activeCount >= maxActiveEvents {
			writeError(writer, http.StatusConflict, "capacity_limit")
			return
		}
		var creatorID *int64
		if body.CreatorTelegramID > 0 {
			_ = tx.QueryRow(ctx, "SELECT person_id FROM telegram_identities WHERE telegram_user_id=$1", body.CreatorTelegramID).Scan(&creatorID)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO events(title,starts_at,ends_at,created_by_person_id) VALUES($1,$2,$3,$4) RETURNING id`, strings.TrimSpace(body.Title), start, end, creatorID).Scan(&eventID); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_publication_references(event_id,provider,external_publication_id,published) VALUES($1,'hikari_publishing',$2,FALSE)`, eventID, strconv.FormatInt(body.PublicationID, 10)); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
	} else if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	} else {
		if _, err := tx.Exec(ctx, `UPDATE events SET title=$2,starts_at=$3,ends_at=$4,status='active',cancelled_at=NULL,updated_at=NOW() WHERE id=$1`, eventID, strings.TrimSpace(body.Title), start, end); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE event_publication_references SET published=FALSE WHERE event_id=$1`, eventID); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	event, err := h.event(ctx, body.PublicationID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, event)
}

func (h *Handler) getEvent(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	event, err := h.event(request.Context(), publicationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, event)
}

func (h *Handler) event(ctx context.Context, publicationID int64) (map[string]any, error) {
	var (
		eventID, externalID int64
		title, status       string
		start, end          time.Time
		published           bool
		registrations       int64
	)
	err := h.pool.QueryRow(ctx, `
		SELECT e.id,e.title,e.starts_at,e.ends_at,e.status,r.external_publication_id::bigint,r.published,
			(SELECT COUNT(*) FROM event_registrations er WHERE er.event_id=e.id AND er.status='registered')
		FROM events e JOIN event_publication_references r ON r.event_id=e.id
		WHERE r.external_publication_id=$1
		ORDER BY CASE r.provider WHEN 'hikari_publishing' THEN 0 ELSE 1 END LIMIT 1`, strconv.FormatInt(publicationID, 10)).Scan(&eventID, &title, &start, &end, &status, &externalID, &published, &registrations)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": eventID, "post_id": externalID, "title": title, "text": title, "starts_at": start.UTC(), "ends_at": end.UTC(), "status": status, "published": published, "registrations": registrations}, nil
}

func (h *Handler) deleteEvent(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	commandTag, err := h.pool.Exec(request.Context(), `DELETE FROM events WHERE id=(SELECT event_id FROM event_publication_references WHERE external_publication_id=$1 LIMIT 1)`, strconv.FormatInt(publicationID, 10))
	if err != nil {
		if strings.Contains(err.Error(), "event_registrations") {
			writeError(writer, http.StatusConflict, "event_has_registrations")
			return
		}
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	if commandTag.RowsAffected() == 0 {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) markPublished(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	commandTag, err := h.pool.Exec(request.Context(), `UPDATE event_publication_references SET published=TRUE WHERE external_publication_id=$1`, strconv.FormatInt(publicationID, 10))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	if commandTag.RowsAffected() == 0 {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listEvents(writer http.ResponseWriter, request *http.Request) {
	view := request.URL.Query().Get("view")
	if view == "active_count" {
		var count int64
		if err := h.pool.QueryRow(request.Context(), `SELECT COUNT(*) FROM events WHERE status='active' AND ends_at>NOW()`).Scan(&count); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"count": count, "limit": maxActiveEvents, "limit_reached": count >= maxActiveEvents})
		return
	}
	if view == "available" {
		telegramID, err := strconv.ParseInt(request.URL.Query().Get("telegram_id"), 10, 64)
		if err != nil || telegramID <= 0 {
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		rows, err := h.pool.Query(request.Context(), `
			SELECT r.external_publication_id::bigint,e.title,e.starts_at,e.ends_at
			FROM events e JOIN event_publication_references r ON r.event_id=e.id
			WHERE e.status='active' AND e.starts_at>NOW() AND r.published=TRUE
			AND NOT EXISTS (SELECT 1 FROM event_registrations er JOIN telegram_identities ti ON ti.person_id=er.person_id WHERE er.event_id=e.id AND er.status='registered' AND ti.telegram_user_id=$1)
			ORDER BY e.starts_at,r.external_publication_id`, telegramID)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		defer rows.Close()
		items, err := scanEventRows(rows)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		writeJSON(writer, http.StatusOK, items)
		return
	}
	past := request.URL.Query().Get("past") == "true"
	page, pageSize, err := parsePage(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	condition := "e.ends_at>NOW()"
	order := "ASC"
	if past {
		condition = "e.ends_at<=NOW()"
		order = "DESC"
	}
	var total int64
	if err := h.pool.QueryRow(request.Context(), `SELECT COUNT(*) FROM events e JOIN event_publication_references r ON r.event_id=e.id WHERE e.status='active' AND `+condition).Scan(&total); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	rows, err := h.pool.Query(request.Context(), `
		SELECT r.external_publication_id::bigint,e.title,e.starts_at,e.ends_at,COUNT(er.id) FILTER (WHERE er.status='registered')
		FROM events e JOIN event_publication_references r ON r.event_id=e.id
		LEFT JOIN event_registrations er ON er.event_id=e.id
		WHERE e.status='active' AND `+condition+`
		GROUP BY r.external_publication_id,e.title,e.starts_at,e.ends_at
		ORDER BY e.starts_at `+order+`,r.external_publication_id `+order+`
		LIMIT $1 OFFSET $2`, pageSize, page*pageSize)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var postID, registrations int64
		var title string
		var start, end time.Time
		if err := rows.Scan(&postID, &title, &start, &end, &registrations); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"post_id": postID, "title": title, "text": title, "starts_at": start.UTC(), "ends_at": end.UTC(), "status": "active", "registrations": registrations})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": pageSize, "pages": maxPages(total, pageSize), "total": total})
}

func (h *Handler) register(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var body struct {
		TelegramUserID        int64 `json:"telegram_user_id"`
		ReminderOffsetMinutes *int  `json:"reminder_offset_minutes"`
	}
	if !decodeJSON(writer, request, &body) || body.TelegramUserID <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var starts time.Time
	err := h.pool.QueryRow(request.Context(), `SELECT e.starts_at FROM events e JOIN event_publication_references r ON r.event_id=e.id WHERE r.external_publication_id=$1 AND e.status='active' AND e.starts_at>NOW() AND r.published=TRUE`, strconv.FormatInt(publicationID, 10)).Scan(&starts)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusConflict, "event_unavailable")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	if body.ReminderOffsetMinutes != nil && (*body.ReminderOffsetMinutes <= 0 || !starts.After(time.Now().UTC().Add(time.Duration(*body.ReminderOffsetMinutes)*time.Minute))) {
		writeError(writer, http.StatusBadRequest, "invalid_reminder")
		return
	}
	var citizen, itmo *bool
	if err := h.pool.QueryRow(request.Context(), `SELECT p.is_russian_citizen,p.is_itmo_student FROM people p JOIN telegram_identities ti ON ti.person_id=p.id WHERE ti.telegram_user_id=$1`, body.TelegramUserID).Scan(&citizen, &itmo); err != nil {
		writeError(writer, http.StatusConflict, "profile_incomplete")
		return
	}
	if citizen == nil || itmo == nil {
		writeError(writer, http.StatusConflict, "profile_incomplete")
		return
	}
	_, err = h.pool.Exec(request.Context(), `
		INSERT INTO event_registrations(event_id,person_id,reminders_enabled,reminder_offset_minutes,status,registered_at)
		SELECT r.event_id,p.id,$3,$4,'registered',NOW()
		FROM event_publication_references r JOIN telegram_identities ti ON ti.telegram_user_id=$1 JOIN people p ON p.id=ti.person_id
		WHERE r.external_publication_id=$2
		ON CONFLICT(event_id,person_id) DO UPDATE SET reminders_enabled=EXCLUDED.reminders_enabled,reminder_offset_minutes=EXCLUDED.reminder_offset_minutes,status='registered',registered_at=NOW(),cancelled_at=NULL,reminder_sent_at=NULL,attendance_prompt_sent_at=NULL`, body.TelegramUserID, strconv.FormatInt(publicationID, 10), body.ReminderOffsetMinutes != nil, body.ReminderOffsetMinutes)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"registered": true})
}

func (h *Handler) registrationStatus(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	telegramID, ok2 := pathInt64(request, "telegram_id")
	if !ok || !ok2 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var status string
	var attended *bool
	err := h.pool.QueryRow(request.Context(), `
		SELECT er.status,a.attended
		FROM event_registrations er
		JOIN event_publication_references r ON r.event_id=er.event_id
		JOIN telegram_identities ti ON ti.person_id=er.person_id
		LEFT JOIN attendance a ON a.registration_id=er.id
		WHERE r.external_publication_id=$1 AND ti.telegram_user_id=$2`, strconv.FormatInt(publicationID, 10), telegramID).Scan(&status, &attended)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": status, "attended": attended})
}

func (h *Handler) cancelRegistration(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	telegramID, ok2 := pathInt64(request, "telegram_id")
	if !ok || !ok2 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := h.pool.Exec(request.Context(), `UPDATE event_registrations er SET status='cancelled',cancelled_at=NOW(),updated_at=NOW() FROM event_publication_references r JOIN telegram_identities ti ON ti.telegram_user_id=$1 WHERE er.event_id=r.event_id AND r.external_publication_id=$2 AND er.person_id=ti.person_id AND er.status='registered' AND EXISTS(SELECT 1 FROM events e WHERE e.id=er.event_id AND e.status='active' AND e.starts_at>NOW())`, telegramID, strconv.FormatInt(publicationID, 10))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"cancelled": result.RowsAffected() == 1})
}

func (h *Handler) userRegistrations(writer http.ResponseWriter, request *http.Request) {
	telegramID, ok := pathInt64(request, "telegram_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	rows, err := h.pool.Query(request.Context(), `
		SELECT r.external_publication_id::bigint,er.status,er.reminders_enabled,er.reminder_offset_minutes,
			a.attended,e.title,e.starts_at,e.ends_at
		FROM event_registrations er JOIN event_publication_references r ON r.event_id=er.event_id
		JOIN events e ON e.id=er.event_id JOIN telegram_identities ti ON ti.person_id=er.person_id
		LEFT JOIN attendance a ON a.registration_id=er.id
		WHERE ti.telegram_user_id=$1 AND er.status='registered' AND e.status='active' AND e.starts_at>NOW()
		ORDER BY e.starts_at`, telegramID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var postID int64
		var status, title string
		var reminders bool
		var offset *int
		var attended *bool
		var start, end time.Time
		if err := rows.Scan(&postID, &status, &reminders, &offset, &attended, &title, &start, &end); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"post_id": postID, "status": status, "reminders_enabled": reminders, "reminder_offset_minutes": offset, "attended": attended, "title": title, "text": title, "starts_at": start.UTC(), "ends_at": end.UTC()})
	}
	writeJSON(writer, http.StatusOK, items)
}

func (h *Handler) eventRegistrations(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	sortBy := request.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "name"
	}
	orderBy := map[string]string{"name": "COALESCE(p.full_name,ti.display_name) ASC", "pass": "COALESCE(p.is_russian_citizen,FALSE) ASC,COALESCE(p.is_itmo_student,FALSE) ASC,COALESCE(p.full_name,ti.display_name) ASC", "registered_at": "er.registered_at ASC"}[sortBy]
	if orderBy == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	page, pageSize, err := parsePage(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	args := []any{strconv.FormatInt(publicationID, 10)}
	var total int64
	if err := h.pool.QueryRow(request.Context(), `SELECT COUNT(*) FROM event_registrations er JOIN event_publication_references r ON r.event_id=er.event_id WHERE r.external_publication_id=$1`, args...).Scan(&total); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	rows, err := h.pool.Query(request.Context(), `
		SELECT ti.telegram_user_id,COALESCE(p.full_name,ti.display_name),ti.username,er.registered_at,er.cancelled_at,er.status,a.attended,p.is_russian_citizen,p.is_itmo_student,e.title
		FROM event_registrations er JOIN event_publication_references r ON r.event_id=er.event_id JOIN events e ON e.id=er.event_id
		JOIN people p ON p.id=er.person_id JOIN telegram_identities ti ON ti.person_id=p.id LEFT JOIN attendance a ON a.registration_id=er.id
		WHERE r.external_publication_id=$1 ORDER BY `+orderBy+`,er.person_id LIMIT $2 OFFSET $3`, strconv.FormatInt(publicationID, 10), pageSize, page*pageSize)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var telegramID int64
		var name string
		var username *string
		var registered, cancelled *time.Time
		var status, title string
		var attended, citizen, itmo *bool
		if err := rows.Scan(&telegramID, &name, &username, &registered, &cancelled, &status, &attended, &citizen, &itmo, &title); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"user_id": telegramID, "full_name": name, "telegram_username": username, "registered_at": registered, "cancelled_at": cancelled, "status": status, "attended": attended, "is_russian_citizen": citizen, "is_itmo_student": itmo, "title": title})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": pageSize, "pages": maxPages(total, pageSize), "total": total})
}

func (h *Handler) cancelEvent(writer http.ResponseWriter, request *http.Request) {
	publicationID, ok := pathInt64(request, "publication_id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	tx, err := h.pool.Begin(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer func() { _ = tx.Rollback(request.Context()) }()
	var eventID int64
	if err := tx.QueryRow(request.Context(), `UPDATE events SET status='cancelled',cancelled_at=NOW(),updated_at=NOW() WHERE id=(SELECT event_id FROM event_publication_references WHERE external_publication_id=$1 LIMIT 1) AND status='active' AND ends_at>NOW() RETURNING id`, strconv.FormatInt(publicationID, 10)).Scan(&eventID); errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	rows, err := tx.Query(request.Context(), `SELECT ti.telegram_user_id,e.title FROM event_registrations er JOIN telegram_identities ti ON ti.person_id=er.person_id JOIN events e ON e.id=er.event_id WHERE er.event_id=$1 AND er.status='registered'`, eventID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"user_id": id, "title": title, "text": title})
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	if err := tx.Commit(request.Context()); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, items)
}

func (h *Handler) dueReminders(writer http.ResponseWriter, request *http.Request) {
	rows, err := h.pool.Query(request.Context(), `SELECT ti.telegram_user_id,r.external_publication_id::bigint,er.reminder_offset_minutes,e.title,e.starts_at FROM event_registrations er JOIN event_publication_references r ON r.event_id=er.event_id JOIN events e ON e.id=er.event_id JOIN telegram_identities ti ON ti.person_id=er.person_id WHERE er.status='registered' AND er.reminders_enabled AND er.reminder_sent_at IS NULL AND e.status='active' AND e.starts_at>NOW() AND e.starts_at<=NOW()+er.reminder_offset_minutes*interval '1 minute'`)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, postID, offset int64
		var title string
		var starts time.Time
		if err := rows.Scan(&id, &postID, &offset, &title, &starts); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"user_id": id, "post_id": postID, "reminder_offset_minutes": offset, "title": title, "text": title, "starts_at": starts.UTC()})
	}
	writeJSON(writer, http.StatusOK, items)
}

func (h *Handler) markReminderSent(writer http.ResponseWriter, request *http.Request) {
	h.markRegistrationTimestamp(writer, request, "reminder_sent_at")
}

func (h *Handler) dueAttendancePrompts(writer http.ResponseWriter, request *http.Request) {
	rows, err := h.pool.Query(request.Context(), `SELECT ti.telegram_user_id,r.external_publication_id::bigint,e.title FROM event_registrations er JOIN event_publication_references r ON r.event_id=er.event_id JOIN events e ON e.id=er.event_id JOIN telegram_identities ti ON ti.person_id=er.person_id WHERE er.status='registered' AND er.attendance_prompt_sent_at IS NULL AND e.status='active' AND e.ends_at<=NOW()`)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, postID int64
		var title string
		if err := rows.Scan(&id, &postID, &title); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"user_id": id, "post_id": postID, "title": title, "text": title})
	}
	writeJSON(writer, http.StatusOK, items)
}

func (h *Handler) markAttendancePromptSent(writer http.ResponseWriter, request *http.Request) {
	h.markRegistrationTimestamp(writer, request, "attendance_prompt_sent_at")
}

func (h *Handler) markRegistrationTimestamp(writer http.ResponseWriter, request *http.Request, column string) {
	var body struct {
		TelegramUserID int64 `json:"telegram_user_id"`
		PublicationID  int64 `json:"publication_id"`
	}
	if !decodeJSON(writer, request, &body) || body.TelegramUserID <= 0 || body.PublicationID <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if column != "reminder_sent_at" && column != "attendance_prompt_sent_at" {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	query := `UPDATE event_registrations er SET ` + column + `=NOW(),updated_at=NOW() FROM event_publication_references r JOIN telegram_identities ti ON ti.telegram_user_id=$1 WHERE er.event_id=r.event_id AND r.external_publication_id=$2`
	result, err := h.pool.Exec(request.Context(), query, body.TelegramUserID, strconv.FormatInt(body.PublicationID, 10))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"updated": result.RowsAffected() == 1})
}

func (h *Handler) recordAttendance(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		TelegramUserID int64 `json:"telegram_user_id"`
		PublicationID  int64 `json:"publication_id"`
		Attended       bool  `json:"attended"`
	}
	if !decodeJSON(writer, request, &body) || body.TelegramUserID <= 0 || body.PublicationID <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := h.pool.Exec(request.Context(), `INSERT INTO attendance(registration_id,attended,recorded_at) SELECT er.id,$3,NOW() FROM event_registrations er JOIN event_publication_references r ON r.event_id=er.event_id JOIN telegram_identities ti ON ti.person_id=er.person_id JOIN events e ON e.id=er.event_id WHERE ti.telegram_user_id=$1 AND r.external_publication_id=$2 AND er.status='registered' AND e.status='active' AND e.ends_at<=NOW() ON CONFLICT(registration_id) DO UPDATE SET attended=EXCLUDED.attended,recorded_at=EXCLUDED.recorded_at`, body.TelegramUserID, strconv.FormatInt(body.PublicationID, 10), body.Attended)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"recorded": result.RowsAffected() == 1})
}

func (h *Handler) saveFeedback(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		TelegramUserID int64  `json:"telegram_user_id"`
		PublicationID  *int64 `json:"publication_id"`
		Text           string `json:"text"`
	}
	if !decodeJSON(writer, request, &body) || body.TelegramUserID <= 0 || utf8.RuneCountInString(strings.TrimSpace(body.Text)) == 0 || utf8.RuneCountInString(body.Text) > 4000 {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx := request.Context()
	var eventID *int64
	if body.PublicationID != nil {
		var id int64
		err := h.pool.QueryRow(ctx, `SELECT r.event_id FROM event_publication_references r JOIN event_registrations er ON er.event_id=r.event_id JOIN telegram_identities ti ON ti.person_id=er.person_id JOIN attendance a ON a.registration_id=er.id WHERE r.external_publication_id=$1 AND ti.telegram_user_id=$2 AND a.attended=TRUE`, strconv.FormatInt(*body.PublicationID, 10), body.TelegramUserID).Scan(&id)
		if err != nil {
			writeError(writer, http.StatusConflict, "feedback_event_unavailable")
			return
		}
		eventID = &id
	}
	var feedbackID int64
	err := h.pool.QueryRow(ctx, `INSERT INTO feedback(person_id,event_id,text) SELECT ti.person_id,$1,$2 FROM telegram_identities ti WHERE ti.telegram_user_id=$3 RETURNING id`, eventID, strings.TrimSpace(body.Text), body.TelegramUserID).Scan(&feedbackID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": feedbackID})
}

func (h *Handler) feedbackEntry(writer http.ResponseWriter, request *http.Request) {
	id, ok := pathInt64(request, "id")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	entry, err := h.feedback(ctxOrBackground(request), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(writer, http.StatusOK, entry)
}

func (h *Handler) feedback(ctx context.Context, id int64) (map[string]any, error) {
	var authorID int64
	var username *string
	var name, text, title *string
	var created time.Time
	var postID *int64
	err := h.pool.QueryRow(ctx, `SELECT ti.telegram_user_id,ti.username,COALESCE(p.full_name,ti.display_name),f.text,f.created_at,r.external_publication_id::bigint,e.title FROM feedback f JOIN people p ON p.id=f.person_id JOIN telegram_identities ti ON ti.person_id=p.id LEFT JOIN events e ON e.id=f.event_id LEFT JOIN event_publication_references r ON r.event_id=e.id WHERE f.id=$1`, id).Scan(&authorID, &username, &name, &text, &created, &postID, &title)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "user_id": authorID, "full_name": name, "telegram_username": username, "feedback_text": text, "created_at": created.UTC(), "post_id": postID, "title": title}, nil
}

func (h *Handler) feedbackPage(writer http.ResponseWriter, request *http.Request) {
	page, size, err := parsePage(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var total int64
	if err := h.pool.QueryRow(request.Context(), `SELECT COUNT(*) FROM feedback`).Scan(&total); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	rows, err := h.pool.Query(request.Context(), `SELECT f.id FROM feedback f ORDER BY f.created_at DESC,f.id DESC LIMIT $1 OFFSET $2`, size, page*size)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		entry, err := h.feedback(request.Context(), id)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, entry)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "pages": maxPages(total, size), "total": total})
}

func (h *Handler) peopleStatistics(writer http.ResponseWriter, request *http.Request) {
	page, size, err := parsePage(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	sortBy := request.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "name"
	}
	order := map[string]string{"name": "COALESCE(p.full_name,ti.display_name) ASC", "visits": "COALESCE(am.visits,0) DESC", "last_visit": "am.last_visit_at DESC NULLS LAST"}[sortBy]
	if order == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	var total int64
	if err := h.pool.QueryRow(request.Context(), `SELECT COUNT(*) FROM people`).Scan(&total); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	rows, err := h.pool.Query(request.Context(), `
		WITH attendance_metrics AS (
			SELECT er.person_id,
				COUNT(*) FILTER (WHERE a.attended) AS visits,
				MAX(e.starts_at) FILTER (WHERE a.attended) AS last_visit_at
			FROM event_registrations er
			LEFT JOIN attendance a ON a.registration_id=er.id
			LEFT JOIN events e ON e.id=er.event_id
			GROUP BY er.person_id
		), feedback_metrics AS (
			SELECT person_id, COUNT(*) AS feedback_count
			FROM feedback
			GROUP BY person_id
		)
		SELECT p.id,p.full_name,ti.telegram_user_id,ti.username,ti.display_name,
			p.is_russian_citizen,p.is_itmo_student,
			COALESCE(am.visits,0),COALESCE(fm.feedback_count,0),am.last_visit_at
		FROM people p
		JOIN telegram_identities ti ON ti.person_id=p.id
		LEFT JOIN attendance_metrics am ON am.person_id=p.id
		LEFT JOIN feedback_metrics fm ON fm.person_id=p.id
		ORDER BY `+order+`,p.id LIMIT $1 OFFSET $2`, size, page*size)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, userID, visits, feedbackCount int64
		var fullName, username *string
		var display string
		var citizen, itmo *bool
		var last *time.Time
		if err := rows.Scan(&id, &fullName, &userID, &username, &display, &citizen, &itmo, &visits, &feedbackCount, &last); err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}
		items = append(items, map[string]any{"id": id, "full_name": fullName, "telegram_user_id": userID, "username": username, "telegram_username": username, "display_name": display, "is_russian_citizen": citizen, "is_itmo_student": itmo, "visits": visits, "feedback_count": feedbackCount, "last_visit_at": last})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "pages": maxPages(total, size), "total": total})
}

func ensureIdentity(ctx context.Context, tx pgx.Tx, telegramID int64, username, displayName string) (int64, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", telegramID); err != nil {
		return 0, err
	}
	var personID int64
	err := tx.QueryRow(ctx, "SELECT person_id FROM telegram_identities WHERE telegram_user_id=$1 FOR UPDATE", telegramID).Scan(&personID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, "INSERT INTO people DEFAULT VALUES RETURNING id").Scan(&personID); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO telegram_identities(person_id,telegram_user_id,username,display_name,last_seen_at) VALUES($1,$2,NULLIF($3,''),$4,NOW()) ON CONFLICT(telegram_user_id) DO UPDATE SET username=EXCLUDED.username,display_name=EXCLUDED.display_name,last_seen_at=NOW(),updated_at=NOW()`, personID, telegramID, username, displayName)
	return personID, err
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func pathInt64(request *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(request.PathValue(name), 10, 64)
	return value, err == nil && value > 0
}
func parseEventTimes(startValue, endValue string) (time.Time, time.Time, bool) {
	start, err1 := time.Parse(time.RFC3339Nano, startValue)
	end, err2 := time.Parse(time.RFC3339Nano, endValue)
	return start.UTC(), end.UTC(), err1 == nil && err2 == nil && start.Before(end)
}
func parsePage(request *http.Request) (int, int, error) {
	page, size := 0, 10
	var err error
	if raw := request.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 0 {
			return 0, 0, fmt.Errorf("invalid page")
		}
	}
	if raw := request.URL.Query().Get("page_size"); raw != "" {
		size, err = strconv.Atoi(raw)
		if err != nil || size < 1 || size > maxPageSize {
			return 0, 0, fmt.Errorf("invalid page size")
		}
	}
	return page, size, nil
}
func maxPages(total int64, size int) int64 {
	if total == 0 {
		return 1
	}
	return (total + int64(size) - 1) / int64(size)
}
func scanEventRows(rows pgx.Rows) ([]map[string]any, error) {
	items := make([]map[string]any, 0)
	for rows.Next() {
		var postID int64
		var title string
		var start, end time.Time
		if err := rows.Scan(&postID, &title, &start, &end); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"post_id": postID, "title": title, "text": title, "starts_at": start.UTC(), "ends_at": end.UTC()})
	}
	return items, rows.Err()
}
func ctxOrBackground(request *http.Request) context.Context {
	if request == nil {
		return context.Background()
	}
	return request.Context()
}
func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]string{"error": code})
}
func writeJSON(writer http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		log.Printf("bot API JSON serialization failed: %v", err)
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":"serialization_error"}`))
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(payload, '\n'))
}

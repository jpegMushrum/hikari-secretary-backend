// Package mascotapi exposes the election inside the authenticated club routers.
package mascotapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/mascot/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
)

type Repository interface {
	Visibility(context.Context, bool) (application.Visibility, error)
	View(context.Context, int64, bool) (application.View, error)
	ResultEntry(context.Context, int64, bool) (application.Entry, error)
	Action(context.Context, int64, bool, application.Command) error
	EndTestSubmissions(context.Context, int64) error
	ResetTestSeason(context.Context, int64) error
	ReserveUpload(context.Context, int64, int64, string) error
	ReleaseReservation(context.Context, int64, string) error
	DiscardOwnedUpload(context.Context, int64, string) error
	FinishUpload(context.Context, int64, string) error
	DiscardUpload(context.Context, int64, string) error
	CanReadImage(context.Context, int64, bool, string) (bool, error)
}

type Objects interface {
	Put(context.Context, string, []byte, string) error
	Get(context.Context, string) ([]byte, string, error)
	Delete(context.Context, string) error
}

type Handler struct {
	Repo    Repository
	Objects Objects
}

func (h *Handler) MountAdmin(mux *http.ServeMux, local bool) {
	const base = "/api/v1/admin/mascot"
	h.mount(mux, base, true, func(r *http.Request) int64 {
		identity, ok := requestcontext.Administrator(r.Context())
		if !ok {
			return 0
		}
		return identity.TelegramID
	})
	if local {
		mux.HandleFunc("POST "+base+"/test-deadline", localSeasonAction(h.Repo.EndTestSubmissions))
		mux.HandleFunc("POST "+base+"/test-reset", localSeasonAction(h.Repo.ResetTestSeason))
	}
}

func localSeasonAction(action func(context.Context, int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SeasonID int64 `json:"season_id"`
		}
		if decode(w, r, &body) {
			respond(w, nil, action(r.Context(), body.SeasonID))
		}
	}
}

func (h *Handler) MountMember(mux *http.ServeMux, personID func(*http.Request) int64) {
	const base = "/api/v1/member/mascot"
	h.mount(mux, base, false, personID)
	mux.HandleFunc("POST "+base+"/images", func(w http.ResponseWriter, r *http.Request) { h.upload(w, r, personID(r)) })
	mux.HandleFunc("DELETE "+base+"/images/{key}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, nil, h.Repo.DiscardOwnedUpload(r.Context(), personID(r), r.PathValue("key")))
	})
}

func (h *Handler) mount(mux *http.ServeMux, base string, admin bool, actor func(*http.Request) int64) {
	mux.HandleFunc("GET "+base+"/visibility", func(w http.ResponseWriter, r *http.Request) {
		visibility, err := h.Repo.Visibility(r.Context(), admin)
		respond(w, visibility, err)
	})
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		view, err := h.Repo.View(r.Context(), actor(r), admin)
		respond(w, view, err)
	})
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var cmd application.Command
		if decode(w, r, &cmd) {
			respond(w, nil, h.Repo.Action(r.Context(), actor(r), admin, cmd))
		}
	})
	mux.HandleFunc("GET "+base+"/images/{key}", func(w http.ResponseWriter, r *http.Request) {
		h.image(w, r, actor(r), admin)
	})
	mux.HandleFunc("GET "+base+"/results/entries/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			respond(w, nil, domain.ErrInvalid)
			return
		}
		entry, err := h.Repo.ResultEntry(r.Context(), id, admin)
		respond(w, entry, err)
	})
}

func decode(w http.ResponseWriter, r *http.Request, body any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(body) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		respond(w, nil, domain.ErrInvalid)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Cache-Control", "private, no-store")
	if err == nil && data == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	status := http.StatusOK
	if err != nil {
		code := "internal_error"
		status = http.StatusInternalServerError
		switch {
		case errors.Is(err, domain.ErrInvalid):
			status, code = 400, "invalid_request"
		case errors.Is(err, domain.ErrForbidden):
			status, code = 403, "forbidden"
		case errors.Is(err, domain.ErrNotFound):
			status, code = 404, "not_found"
		case errors.Is(err, domain.ErrConflict):
			status, code = 409, "invalid_state"
		case errors.Is(err, errMediaUnavailable):
			status, code = 503, "media_unavailable"
		case errors.Is(err, errTooLarge):
			status, code = 413, "image_too_large"
		}
		data = map[string]string{"error": code}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

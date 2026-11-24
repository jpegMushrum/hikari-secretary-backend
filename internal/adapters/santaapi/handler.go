// Package santaapi mounts seasonal routes behind the existing role middleware.
package santaapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/santa"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/requestcontext"
)

// MountAdmin is called by the authenticated administrative router.
func MountAdmin(mux *http.ServeMux, service *santa.Service) {
	const base = "/api/v1/admin/santa"
	mux.HandleFunc("GET "+base+"/visibility", visibility(service, true))
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		view, err := service.Admin(r.Context())
		respond(w, view, err)
	})
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var cmd santa.AdminCommand
		if !decode(w, r, &cmd) {
			return
		}
		admin, ok := requestcontext.Administrator(r.Context())
		if !ok {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		cmd.ActorTelegramID = admin.TelegramID
		respond(w, nil, service.AdminAction(r.Context(), cmd))
	})
}

// MountLocalReset is mounted only in the synthetic local admin router.
func MountLocalReset(mux *http.ServeMux, service *santa.Service) {
	mux.HandleFunc("POST /api/v1/admin/santa/reset", func(w http.ResponseWriter, r *http.Request) {
		var cmd struct {
			SeasonID int64 `json:"season_id"`
			Revision int64 `json:"revision"`
		}
		admin, ok := requestcontext.Administrator(r.Context())
		if !ok {
			write(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if decode(w, r, &cmd) {
			respond(w, nil, service.Reset(r.Context(), cmd.SeasonID, cmd.Revision, admin.TelegramID))
		}
	})
}

// MountMember obtains the owner exclusively from the verified member context.
func MountMember(mux *http.ServeMux, service *santa.Service, personID func(*http.Request) int64) {
	const base = "/api/v1/member/santa"
	mux.HandleFunc("GET "+base+"/visibility", visibility(service, false))
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		view, err := service.Member(r.Context(), personID(r))
		respond(w, view, err)
	})
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var cmd santa.MemberCommand
		if decode(w, r, &cmd) {
			respond(w, nil, service.MemberAction(r.Context(), personID(r), cmd))
		}
	})
}

func visibility(service *santa.Service, admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		season, err := service.VisibleSeason(r.Context(), admin)
		data := map[string]any{"visible": season != nil}
		if season != nil {
			data["confirmed"] = season.Confirmed
			if season.ExchangeAt != nil {
				data["exchange_at"] = season.ExchangeAt
			}
			if season.RegistrationUntil != nil {
				data["registration_until"] = season.RegistrationUntil
			}
		}
		respond(w, data, err)
	}
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		respond(w, nil, santa.ErrInvalid)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, data any, err error) {
	if err != nil {
		status, code := http.StatusInternalServerError, "internal_error"
		switch {
		case errors.Is(err, santa.ErrInvalid):
			status, code = http.StatusBadRequest, "invalid_request"
		case errors.Is(err, santa.ErrNotFound):
			status, code = http.StatusNotFound, "not_found"
		case errors.Is(err, santa.ErrConflict):
			status, code = http.StatusConflict, "invalid_state"
		}
		write(w, status, map[string]string{"error": code})
		return
	}
	if data == nil {
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	write(w, http.StatusOK, data)
}

func write(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

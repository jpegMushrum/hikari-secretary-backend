package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type Pinger interface {
	Ping(context.Context) error
}

type Authenticator interface {
	Validate(string) (telegramauth.Identity, error)
}

type identityContextKey struct{}

func NewRouter(database Pinger, authenticator Authenticator, adminTelegramIDs map[int64]struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready(database))
	mux.Handle("GET /api/v1/admin/me", requireAdmin(authenticator, adminTelegramIDs, http.HandlerFunc(currentAdministrator)))
	return secureHeaders(mux)
}

func requireAdmin(authenticator Authenticator, adminTelegramIDs map[int64]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		initData, ok := initDataFromAuthorization(request.Header.Get("Authorization"))
		if !ok || authenticator == nil {
			writeError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}

		identity, err := authenticator.Validate(initData)
		if err != nil {
			writeError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		if _, ok := adminTelegramIDs[identity.TelegramID]; !ok {
			writeError(writer, http.StatusForbidden, "forbidden")
			return
		}

		ctx := context.WithValue(request.Context(), identityContextKey{}, identity)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func initDataFromAuthorization(header string) (string, bool) {
	scheme, raw, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "tma") {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	return raw, raw != ""
}

func currentAdministrator(writer http.ResponseWriter, request *http.Request) {
	identity, ok := request.Context().Value(identityContextKey{}).(telegramauth.Identity)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "internal_error")
		return
	}

	writeJSON(writer, http.StatusOK, map[string]any{
		"telegram_id": identity.TelegramID,
		"first_name":  identity.FirstName,
		"last_name":   identity.LastName,
		"username":    identity.Username,
	})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request)
	})
}

func health(writer http.ResponseWriter, _ *http.Request) {
	writeStatus(writer, http.StatusOK, "ok")
}

func ready(database Pinger) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if database == nil || database.Ping(request.Context()) != nil {
			writeStatus(writer, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeStatus(writer, http.StatusOK, "ok")
	}
}

func writeStatus(writer http.ResponseWriter, statusCode int, status string) {
	writeJSON(writer, statusCode, map[string]string{"status": status})
}

func writeError(writer http.ResponseWriter, statusCode int, code string) {
	writeJSON(writer, statusCode, map[string]string{"error": code})
}

func writeJSON(writer http.ResponseWriter, statusCode int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(value)
}

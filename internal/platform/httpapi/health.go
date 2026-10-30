package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/application"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/modules/people/domain"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/pagination"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/serviceauth"
	"github.com/jpegMushrum/hikari-secretary-backend/internal/platform/telegramauth"
)

type Pinger interface {
	Ping(context.Context) error
}

type Authenticator interface {
	Validate(string) (telegramauth.Identity, error)
}

type PeopleService interface {
	SyncTelegramIdentity(context.Context, domain.SyncIdentityCommand) (domain.TelegramIdentity, error)
	ListPeople(context.Context, application.ListPeopleQuery) (application.ListPeopleResult, error)
}

type identityContextKey struct{}

func NewRouter(
	database Pinger,
	authenticator Authenticator,
	adminTelegramIDs map[int64]struct{},
	botAuthenticator *serviceauth.Validator,
	people PeopleService,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready(database))
	mux.Handle("GET /api/v1/admin/me", requireAdmin(authenticator, adminTelegramIDs, http.HandlerFunc(currentAdministrator)))
	mux.Handle("GET /api/v1/admin/people", requireAdmin(authenticator, adminTelegramIDs, http.HandlerFunc(listPeople(people))))
	mux.Handle("POST /internal/bot/v1/people/identity", requireBotService(botAuthenticator, http.HandlerFunc(syncTelegramIdentity(people))))
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

func requireBotService(authenticator *serviceauth.Validator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if authenticator == nil || !authenticator.ValidateAuthorization(request.Header.Get("Authorization")) {
			writeError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(writer, request)
	})
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

func listPeople(people PeopleService) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if people == nil {
			writeError(writer, http.StatusServiceUnavailable, "service_unavailable")
			return
		}

		query, err := parsePeopleQuery(request.URL.Query())
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}

		result, err := people.ListPeople(request.Context(), query)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}

		totalPages := (result.TotalItems + int64(query.Page.Size) - 1) / int64(query.Page.Size)
		writeJSON(writer, http.StatusOK, map[string]any{
			"items": result.Items,
			"page": map[string]any{
				"number":      query.Page.Number,
				"size":        query.Page.Size,
				"total_items": result.TotalItems,
				"total_pages": totalPages,
			},
			"sort": map[string]string{
				"field": string(query.Sort),
				"order": string(query.Order),
			},
			"filters": map[string]any{
				"search":             query.Search,
				"is_russian_citizen": query.IsRussianCitizen,
				"is_itmo_student":    query.IsITMOStudent,
			},
		})
	}
}

func parsePeopleQuery(values url.Values) (application.ListPeopleQuery, error) {
	page, err := pagination.Parse(values)
	if err != nil {
		return application.ListPeopleQuery{}, err
	}

	search, err := singleQueryValue(values, "search")
	if err != nil || utf8.RuneCountInString(search) > 100 {
		return application.ListPeopleQuery{}, errors.New("invalid search")
	}

	sort, err := parsePeopleSort(values)
	if err != nil {
		return application.ListPeopleQuery{}, err
	}
	order, err := parseSortOrder(values, sort)
	if err != nil {
		return application.ListPeopleQuery{}, err
	}

	isRussianCitizen, err := optionalBool(values, "is_russian_citizen")
	if err != nil {
		return application.ListPeopleQuery{}, err
	}
	isITMOStudent, err := optionalBool(values, "is_itmo_student")
	if err != nil {
		return application.ListPeopleQuery{}, err
	}

	return application.ListPeopleQuery{
		Page:             page,
		Search:           strings.TrimSpace(search),
		IsRussianCitizen: isRussianCitizen,
		IsITMOStudent:    isITMOStudent,
		Sort:             sort,
		Order:            order,
	}, nil
}

func parsePeopleSort(values url.Values) (application.SortField, error) {
	raw, err := singleQueryValue(values, "sort")
	if err != nil {
		return "", err
	}
	if raw == "" {
		return application.SortByName, nil
	}

	sort := application.SortField(raw)
	switch sort {
	case application.SortByName, application.SortByVisits, application.SortByLastVisit:
		return sort, nil
	default:
		return "", errors.New("unsupported sort")
	}
}

func parseSortOrder(values url.Values, sort application.SortField) (application.SortOrder, error) {
	raw, err := singleQueryValue(values, "order")
	if err != nil {
		return "", err
	}
	if raw == "" {
		if sort == application.SortByName {
			return application.SortAscending, nil
		}
		return application.SortDescending, nil
	}

	order := application.SortOrder(raw)
	if order != application.SortAscending && order != application.SortDescending {
		return "", errors.New("unsupported order")
	}
	return order, nil
}

func optionalBool(values url.Values, name string) (*bool, error) {
	raw, err := singleQueryValue(values, name)
	if err != nil || raw == "" {
		return nil, err
	}
	if raw != "true" && raw != "false" {
		return nil, errors.New("invalid boolean")
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func singleQueryValue(values url.Values, name string) (string, error) {
	entries, found := values[name]
	if !found {
		return "", nil
	}
	if len(entries) != 1 {
		return "", errors.New("repeated query parameter")
	}
	return entries[0], nil
}

func syncTelegramIdentity(people PeopleService) http.HandlerFunc {
	type requestBody struct {
		TelegramUserID int64  `json:"telegram_user_id"`
		Username       string `json:"username"`
		DisplayName    string `json:"display_name"`
	}

	return func(writer http.ResponseWriter, request *http.Request) {
		if people == nil {
			writeError(writer, http.StatusServiceUnavailable, "service_unavailable")
			return
		}

		request.Body = http.MaxBytesReader(writer, request.Body, 16<<10)
		defer request.Body.Close()
		var body requestBody
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(writer, http.StatusBadRequest, "invalid_request")
			return
		}

		identity, err := people.SyncTelegramIdentity(request.Context(), domain.SyncIdentityCommand{
			TelegramUserID: body.TelegramUserID,
			Username:       body.Username,
			DisplayName:    body.DisplayName,
		})
		if err != nil {
			if errors.Is(err, domain.ErrInvalidTelegramUserID) ||
				errors.Is(err, domain.ErrInvalidDisplayName) ||
				errors.Is(err, domain.ErrInvalidUsername) {
				writeError(writer, http.StatusBadRequest, "invalid_request")
				return
			}
			writeError(writer, http.StatusInternalServerError, "internal_error")
			return
		}

		writeJSON(writer, http.StatusOK, map[string]any{
			"person_id":        identity.PersonID,
			"telegram_user_id": identity.TelegramUserID,
			"username":         identity.Username,
			"display_name":     identity.DisplayName,
		})
	}
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

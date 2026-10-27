package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

type Pinger interface {
	Ping(context.Context) error
}

func NewRouter(database Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready(database))
	return mux
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
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": status})
}

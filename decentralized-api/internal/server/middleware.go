package server

import (
	"log/slog"
	"net/http"
)

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("Received request", "method", r.Method, "path", r.URL.Path)
		slog.Debug("Request headers", "headers", r.Header)
		next.ServeHTTP(w, r)
	})
}

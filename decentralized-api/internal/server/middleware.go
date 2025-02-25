package server

import (
	"decentralized-api/logging"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logging.Info("Received request", types.Server, "method", r.Method, "path", r.URL.Path)
		logging.Debug("Request headers", types.Server, "headers", r.Header)
		next.ServeHTTP(w, r)
	})
}

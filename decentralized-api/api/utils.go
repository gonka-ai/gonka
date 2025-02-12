package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func RespondWithJson(w http.ResponseWriter, response interface{}) {
	respBytes, err := json.Marshal(response)
	if err != nil {
		slog.Error("Failed to marshal response", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}

func parseJsonBody[T any](r *http.Request) (T, error) {
	var t T
	defer r.Body.Close()

	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		slog.Error("failed to decode request body",
			"path", r.URL.Path,
			"error", err,
		)
		return t, err
	}

	return t, nil
}

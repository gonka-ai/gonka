package health

import (
	"encoding/json"
	"net/http"
)

type StatusEntry struct {
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Status string `json:"status"`
}

// Handler returns an http.HandlerFunc that writes the health status as JSON.
func Handler(statusFn func() []StatusEntry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statusFn())
	}
}

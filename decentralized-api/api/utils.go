package api

import (
	"decentralized-api/logging"
	"encoding/json"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
)

func RespondWithJson(w http.ResponseWriter, response interface{}) {
	respBytes, err := json.Marshal(response)
	if err != nil {
		logging.Error("Failed to marshal response", types.System, "error", err)
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
		logging.Error("failed to decode request body",
			types.System, "path", r.URL.Path,
			"error", err,
		)
		return t, err
	}

	return t, nil
}

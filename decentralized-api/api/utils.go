package api

import (
	"encoding/json"
	"net/http"
)

func RespondWithJson(w http.ResponseWriter, response interface{}) {
	respBytes, err := json.Marshal(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}

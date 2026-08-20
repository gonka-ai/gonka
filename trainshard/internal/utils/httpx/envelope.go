package httpx

import (
	"encoding/json"
	"net/http"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
)

func Write(w http.ResponseWriter, requestID string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		writeEnvelope(w, http.StatusInternalServerError, contract.Envelope{
			Error: &contract.Error{Code: shared.CodeInternal, Message: "cannot encode response"},
			Meta:  contract.Meta{RequestID: requestID},
		})
		return
	}
	writeEnvelope(w, http.StatusOK, contract.Envelope{
		OK:   true,
		Data: payload,
		Meta: contract.Meta{RequestID: requestID},
	})
}

func WriteError(w http.ResponseWriter, requestID string, err error) {
	status, code := StatusOf(err)
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "internal error"
	}
	writeEnvelope(w, status, contract.Envelope{
		Error: &contract.Error{Code: code, Message: message},
		Meta:  contract.Meta{RequestID: requestID},
	})
}

func writeEnvelope(w http.ResponseWriter, status int, envelope contract.Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

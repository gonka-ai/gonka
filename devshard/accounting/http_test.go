package accounting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerFiltersCurrentEpochAndRoutes(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-a", 12, "model-a", EscrowActive)
	registerTestEscrow(t, book, "escrow-b", 12, "model-b", EscrowActive)
	registerTestEscrow(t, book, "escrow-old", 11, "model-a", EscrowSettled)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-a", Nonce: 1}))
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-b", Nonce: 1}))

	handler := NewHandler(book, func(context.Context) (uint64, error) {
		return 12, nil
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/epochs/current/participants?model=model-a&escrow_id=escrow-a,missing&escrow_id=escrow-b",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "rate")
	var collection struct {
		EpochIndex   uint64              `json:"epoch_index"`
		Participants []ParticipantRecord `json:"participants"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &collection))
	require.Equal(t, uint64(12), collection.EpochIndex)
	require.Len(t, collection.Participants, 2)
	require.NotContains(t, response.Body.String(), `"key":`,
		"counter dimensions must be flat API fields, not an internal map key")
	require.Contains(t, response.Body.String(), `"disposition":"protocol_only"`)
	for _, record := range collection.Participants {
		require.Equal(t, "model-a", record.Model)
		require.Len(t, record.LatestNonces, 1)
		require.Equal(t, "escrow-a", record.LatestNonces[0].EscrowID)
	}

	request = httptest.NewRequest(
		http.MethodGet,
		"/api/v1/epochs/12/participants/participant-1?model=model-a",
		nil,
	)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var detail struct {
		Participant string              `json:"participant"`
		Records     []ParticipantRecord `json:"records"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	require.Equal(t, "participant-1", detail.Participant)
	require.Len(t, detail.Records, 1)

	request = httptest.NewRequest(http.MethodGet, "/api/v1/epochs?model=model-a", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	var epochList struct {
		Epochs []EpochSummary `json:"epochs"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &epochList))
	require.Len(t, epochList.Epochs, 2)

	request = httptest.NewRequest(http.MethodPost, "/api/v1/epochs", strings.NewReader("{}"))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)

	response = httptest.NewRecorder()
	NewHandler(book, nil).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/epochs/current/participants", nil,
	))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

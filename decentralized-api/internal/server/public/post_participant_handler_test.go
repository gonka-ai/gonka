package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostParticipantsRouteReturnsDeprecated(t *testing.T) {
	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil)

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/participants",
		strings.NewReader(`{"address":"gonka1test","pub_key":"Aq8=","url":"https://example.com"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusGone, rec.Code)
	require.Equal(t, "true", rec.Header().Get("Deprecation"))

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "deprecated", body["error"])
	require.Contains(t, body["message"], "POST /v1/participants is deprecated")
	require.Contains(t, body["message"], "MsgSubmitNewParticipant")
}

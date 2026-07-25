package gatewayphase_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"devshard/testenv/gatewayphase"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestMount_EpochLatestInferencePhase(t *testing.T) {
	e := echo.New()
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{BlockHeight: 200, EpochIndex: 3})

	req := httptest.NewRequest(http.MethodGet, "/v1/epochs/latest", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "Inference", body["phase"])
	require.Equal(t, false, body["is_confirmation_poc_active"])
}

func TestMount_CurrentParticipantsEmpty(t *testing.T) {
	e := echo.New()
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{})

	req := httptest.NewRequest(http.MethodGet, "/v1/epochs/current/participants", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	active, ok := body["active_participants"].(map[string]any)
	require.True(t, ok)
	participants, ok := active["participants"].([]any)
	require.True(t, ok)
	require.Empty(t, participants)
}

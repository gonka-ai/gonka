package gatewayphase_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPreservedModelsFollowPhaseAndReset(t *testing.T) {
	e := echo.New()
	schedule := gatewayphase.NewSchedule([]gatewayphase.Step{{Phase: gatewayphase.PhasePoCGenerate, Models: []string{"m"}, PreservedParticipants: []string{"p0"}}})
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{Schedule: schedule, Source: func(context.Context) (gatewayphase.State, error) {
		return gatewayphase.State{Participants: []gatewayphase.Participant{{Address: "p0"}, {Address: "p1"}}}, nil
	}})
	read := func() string {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/epochs/current/participants", nil))
		require.Equal(t, http.StatusOK, r.Code)
		return r.Body.String()
	}
	body := read()
	require.Contains(t, body, `"models":["m"]`)
	require.Equal(t, 1, strings.Count(body, `"timeslot_allocation":[true,true]`))
	require.Equal(t, 1, strings.Count(body, `"timeslot_allocation":[true,false]`))
	schedule.Set(nil)
	require.NotContains(t, read(), `"timeslot_allocation":[true,true]`)
}

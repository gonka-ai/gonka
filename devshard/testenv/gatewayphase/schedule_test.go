package gatewayphase_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devshard/testenv/gatewayphase"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func getJSON(t *testing.T, e *echo.Echo, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// Test flow:
// 1. Mount the stub with a fake clock, a three-step PoC schedule and a mutable chain source.
// 2. Step the clock and require the reported phase to follow the schedule, then hold Inference.
// 3. Bump the source epoch and block height; require latest_epoch and epoch_stages to follow.
// 4. Require the participants list to carry the source participants with a poc_weight that gives the gateway at least 1024 concurrent requests each.
func TestMount_PhaseFollowsScheduleAndEpochFollowsSource(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	steps, err := gatewayphase.ParseSteps("10s=PoCGenerate,40s=PoCValidate,60s=Inference")
	require.NoError(t, err)
	schedule := gatewayphase.NewScheduleWithClock(steps, clock.Now)

	var mu sync.Mutex
	state := gatewayphase.State{
		BlockHeight: 150, EpochIndex: 1, PoCStartBlockHeight: 100,
		Participants: []gatewayphase.Participant{{Address: "gonka1p0", InferenceURL: "http://versiond-router:8080"}, {Address: "gonka1p1", InferenceURL: "http://versiond-2:8080"}},
	}
	e := echo.New()
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{
		Schedule: schedule,
		Source: func(context.Context) (gatewayphase.State, error) {
			mu.Lock()
			defer mu.Unlock()
			return state, nil
		},
	})

	for _, tc := range []struct {
		advance time.Duration
		phase   string
	}{
		{0, "Inference"}, {10 * time.Second, "PoCGenerate"}, {29 * time.Second, "PoCGenerate"},
		{time.Second, "PoCValidate"}, {20 * time.Second, "Inference"}, {time.Hour, "Inference"},
	} {
		clock.Advance(tc.advance)
		body := getJSON(t, e, "/v1/epochs/latest")
		require.Equal(t, tc.phase, body["phase"])
		require.Equal(t, false, body["is_confirmation_poc_active"])
	}

	mu.Lock()
	state.EpochIndex, state.BlockHeight, state.PoCStartBlockHeight = 2, 420, 400
	mu.Unlock()
	body := getJSON(t, e, "/v1/epochs/latest")
	require.Equal(t, "420", body["block_height"])
	require.Equal(t, "2", body["latest_epoch"].(map[string]any)["index"])
	require.Equal(t, "400", body["latest_epoch"].(map[string]any)["poc_start_block_height"])
	require.Equal(t, "3", body["next_epoch_stages"].(map[string]any)["epoch_index"])

	participants := getJSON(t, e, "/v1/epochs/current/participants")["active_participants"].(map[string]any)["participants"].([]any)
	require.Len(t, participants, 2)
	first := participants[0].(map[string]any)
	require.Equal(t, "gonka1p0", first["index"])
	require.Equal(t, "http://versiond-router:8080", first["inference_url"])
	node := first["ml_nodes"].([]any)[0].(map[string]any)["ml_nodes"].([]any)[0].(map[string]any)
	weight, err := strconv.ParseFloat(node["poc_weight"].(string), 64)
	require.NoError(t, err)
	// The gateway allows floor(weight * 5 / 10000) requests per participant at its default rate.
	require.GreaterOrEqual(t, math.Floor(weight*5/10000), 1024.0)
}

// Test flow:
// 1. Mount the static stub and POST /testenv/phase with a confirmation PoC step at 0s.
// 2. Require is_confirmation_poc_active and the active event phase in /v1/epochs/latest.
// 3. Require GET /testenv/phase to report the current step.
// 4. POST an empty schedule and require Inference with no confirmation PoC.
// 5. Require unknown phases and bad durations to be rejected with 400.
func TestMount_PostPhaseScheduleConfirmationPoC(t *testing.T) {
	e := echo.New()
	gatewayphase.Mount(e.Group(""), gatewayphase.Config{})
	post := func(body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/testenv/phase", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, post(`{"steps":[{"at":"0s","phase":"Inference","confirmation_poc_phase":"CONFIRMATION_POC_GENERATION"}]}`))
	body := getJSON(t, e, "/v1/epochs/latest")
	require.Equal(t, "Inference", body["phase"])
	require.Equal(t, true, body["is_confirmation_poc_active"])
	require.Equal(t, "CONFIRMATION_POC_GENERATION", body["active_confirmation_poc_event"].(map[string]any)["phase"])
	current := getJSON(t, e, "/testenv/phase")["current"].(map[string]any)
	require.Equal(t, "CONFIRMATION_POC_GENERATION", current["confirmation_poc_phase"])

	require.Equal(t, http.StatusOK, post(`{"steps":[]}`))
	body = getJSON(t, e, "/v1/epochs/latest")
	require.Equal(t, "Inference", body["phase"])
	require.Equal(t, false, body["is_confirmation_poc_active"])
	require.NotContains(t, body, "active_confirmation_poc_event")

	require.Equal(t, http.StatusBadRequest, post(`{"steps":[{"at":"0s","phase":"Sleeping"}]}`))
	require.Equal(t, http.StatusBadRequest, post(`{"steps":[{"at":"soon","phase":"PoCGenerate"}]}`))
	_, err := gatewayphase.ParseSteps("PoCGenerate")
	require.Error(t, err)
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
)

func TestDebugTimeoutClockAfterMismatch(t *testing.T) {
	legacy := false
	g := &Gateway{runtimeOrder: []*devshardRuntime{{
		id:               "escrow-b7",
		testTimeoutClock: &legacy,
	}}}

	before := headertime.TimeoutClockDebug().MismatchTotal
	headertime.RecordClockMismatch("gateway", "execution", "escrow-b7", 15, headertime.DueEval{
		Active:    headertime.DueSide{Due: true, Wall: true},
		Shadow:    headertime.DueSide{Due: false},
		HasShadow: true,
		Mismatch:  true,
	}, 1_700_000_100, 1_700_100_000_000, 77)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/timeout-clock", nil)
	rec := httptest.NewRecorder()
	g.handleDebugTimeoutClock(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		MismatchTotal          float64                         `json:"mismatch_total"`
		HeightsyncTimeoutClock *bool                           `json:"heightsync_timeout_clock"`
		LastMismatch           *headertime.ClockMismatchSample `json:"last_mismatch"`
		Escrows                []struct {
			Escrow                 string                          `json:"escrow"`
			HeightsyncTimeoutClock bool                            `json:"heightsync_timeout_clock"`
			LastMismatch           *headertime.ClockMismatchSample `json:"last_mismatch"`
		} `json:"escrows"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.HeightsyncTimeoutClock)
	require.False(t, *body.HeightsyncTimeoutClock)
	require.GreaterOrEqual(t, body.MismatchTotal, before+1)
	require.GreaterOrEqual(t, body.MismatchTotal, 1.0)
	require.NotNil(t, body.LastMismatch)
	require.True(t, body.LastMismatch.WallDue)
	require.False(t, body.LastMismatch.StampDue)
	require.Equal(t, "escrow-b7", body.LastMismatch.Escrow)
	require.Equal(t, uint64(15), body.LastMismatch.Nonce)
	require.Equal(t, "execution", body.LastMismatch.Reason)
	require.Equal(t, int64(1_700_000_100), body.LastMismatch.WallAnchor)
	require.Equal(t, int64(1_700_100_000_000), body.LastMismatch.StampMs)
	require.Len(t, body.Escrows, 1)
	require.Equal(t, "escrow-b7", body.Escrows[0].Escrow)
	require.False(t, body.Escrows[0].HeightsyncTimeoutClock)
	require.NotNil(t, body.Escrows[0].LastMismatch)
	require.Equal(t, uint64(15), body.Escrows[0].LastMismatch.Nonce)
}

func TestDebugTimeoutClockOmitsModeWhenSeveralEscrows(t *testing.T) {
	legacy := false
	header := true
	g := &Gateway{runtimeOrder: []*devshardRuntime{
		{id: "a", testTimeoutClock: &legacy},
		{id: "b", testTimeoutClock: &header},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/debug/timeout-clock", nil)
	rec := httptest.NewRecorder()
	g.handleDebugTimeoutClock(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, ok := body["heightsync_timeout_clock"]
	require.False(t, ok, "one process mode is only present for a single escrow")
	escrows, ok := body["escrows"].([]any)
	require.True(t, ok)
	require.Len(t, escrows, 2)
}

// The latest sample is attached only to the escrow it names. Another escrow
// keeps its own clock mode and does not inherit that sample.
func TestDebugTimeoutClockKeepsLastMismatchOnItsEscrow(t *testing.T) {
	legacy := false
	header := true
	g := &Gateway{runtimeOrder: []*devshardRuntime{
		{id: "escrow-a", testTimeoutClock: &header},
		{id: "escrow-b", testTimeoutClock: &legacy},
	}}
	headertime.RecordClockMismatch("gateway", "refused", "escrow-b", 4, headertime.DueEval{
		Active:    headertime.DueSide{Due: true, Wall: true},
		Shadow:    headertime.DueSide{Due: false},
		HasShadow: true,
		Mismatch:  true,
		Cause:     "stamp_late",
	}, 1_700_000_000, 1_800_000_000_000, 9)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/timeout-clock", nil)
	rec := httptest.NewRecorder()
	g.handleDebugTimeoutClock(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		LastMismatch *headertime.ClockMismatchSample `json:"last_mismatch"`
		Escrows      []struct {
			Escrow                 string                          `json:"escrow"`
			HeightsyncTimeoutClock bool                            `json:"heightsync_timeout_clock"`
			LastMismatch           *headertime.ClockMismatchSample `json:"last_mismatch"`
		} `json:"escrows"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.LastMismatch)
	require.Equal(t, "escrow-b", body.LastMismatch.Escrow)
	require.Len(t, body.Escrows, 2)
	require.Equal(t, "escrow-a", body.Escrows[0].Escrow)
	require.True(t, body.Escrows[0].HeightsyncTimeoutClock)
	require.Nil(t, body.Escrows[0].LastMismatch)
	require.Equal(t, "escrow-b", body.Escrows[1].Escrow)
	require.False(t, body.Escrows[1].HeightsyncTimeoutClock)
	require.NotNil(t, body.Escrows[1].LastMismatch)
	require.Equal(t, uint64(4), body.Escrows[1].LastMismatch.Nonce)
	require.Equal(t, "stamp_late", body.Escrows[1].LastMismatch.MismatchCause)
}

func TestGatewayMetricsScrapeIncludesClockMismatch(t *testing.T) {
	metrics := NewDevshardMetrics()
	headertime.RegisterTimeoutClockMismatch(metrics.registry)
	before, _ := clockMismatchValue(metrics)
	headertime.RecordClockMismatch("gateway", "refused", "escrow-metrics", 1, headertime.DueEval{
		Active:    headertime.DueSide{Due: true, Wall: true},
		Shadow:    headertime.DueSide{Due: false},
		HasShadow: true,
		Mismatch:  true,
		Cause:     "stamp_late",
	}, 1_700_000_000, 0, 1)
	got, ok := clockMismatchValue(metrics)
	require.True(t, ok)
	require.Equal(t, before+1, got)
}

func clockMismatchValue(metrics *DevshardMetrics) (float64, bool) {
	families, err := metrics.registry.Gather()
	if err != nil {
		return 0, false
	}
	for _, family := range families {
		if family.GetName() != "devshard_timeout_clock_mismatch_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["side"] == "gateway" && labels["kind"] == "refused" && metric.GetCounter() != nil {
				return metric.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}

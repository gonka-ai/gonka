package testutil

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// EscrowHostStats is what the escrow state records against the hosts, summed across slots.
type EscrowHostStats struct {
	Missed uint64
	Cost   uint64
}

// EscrowSlotStats is what the escrow state records against one slot.
type EscrowSlotStats struct {
	Slot                 uint64
	Missed               uint64
	Cost                 uint64
	CompletedValidations uint64
}

// WaitEscrowHostStats polls the escrow state until it satisfies ready or timeout expires, and reports what it last read.
func WaitEscrowHostStats(t *testing.T, client *http.Client, escrowURL string, timeout time.Duration, ready func(EscrowHostStats) bool) EscrowHostStats {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last EscrowHostStats
	for time.Now().Before(deadline) {
		last = EscrowHostStats{}
		for _, slot := range EscrowHostStatsBySlot(t, client, escrowURL) {
			last.Missed += slot.Missed
			last.Cost += slot.Cost
		}
		if ready(last) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last
}

// EscrowHostStatsBySlot reads the per-slot host stats the gateway's debug state carries for one escrow.
func EscrowHostStatsBySlot(t *testing.T, client *http.Client, escrowURL string) []EscrowSlotStats {
	t.Helper()
	state := GetJSON(t, client, escrowURL+DebugStateRoute)
	entries, ok := state["host_stats"].([]any)
	require.True(t, ok, "debug state host_stats should be an array, got %T", state["host_stats"])

	stats := make([]EscrowSlotStats, 0, len(entries))
	for _, raw := range entries {
		entry, isObject := raw.(map[string]any)
		require.True(t, isObject, "host_stats entry should be an object, got %T", raw)
		stats = append(stats, EscrowSlotStats{
			Slot:                 optionalNumericField(t, entry, "slot"),
			Missed:               optionalNumericField(t, entry, "Missed"),
			Cost:                 optionalNumericField(t, entry, "Cost"),
			CompletedValidations: optionalNumericField(t, entry, "CompletedValidations"),
		})
	}
	return stats
}

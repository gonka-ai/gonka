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

// WaitEscrowHostStats polls the escrow state until it satisfies ready or timeout expires, and reports what it last read.
func WaitEscrowHostStats(t *testing.T, client *http.Client, clientURL string, timeout time.Duration, ready func(EscrowHostStats) bool) EscrowHostStats {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last EscrowHostStats
	for time.Now().Before(deadline) {
		last = readEscrowHostStats(t, client, clientURL)
		if ready(last) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	return last
}

func readEscrowHostStats(t *testing.T, client *http.Client, clientURL string) EscrowHostStats {
	t.Helper()
	state := GetJSON(t, client, clientURL+"/v1/state")
	perSlot, ok := state["host_stats"].(map[string]any)
	require.True(t, ok, "state host_stats should be an object keyed by slot, got %T", state["host_stats"])

	var stats EscrowHostStats
	for slot, raw := range perSlot {
		entry, isObject := raw.(map[string]any)
		require.True(t, isObject, "host_stats[%s] should be an object, got %T", slot, raw)
		stats.Missed += optionalNumericField(t, entry, "missed")
		stats.Cost += optionalNumericField(t, entry, "cost")
	}
	return stats
}

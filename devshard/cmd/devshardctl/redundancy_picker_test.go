package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunInference_PickerTracksTriedHostsAcrossRetries verifies the
// retry-set is being threaded correctly by examining PerfTracker's
// recorded host list. After a failed primary, the secondary must land
// on a different host, and across the entire request no host index
// should appear twice in the attempt list.
func TestRunInference_PickerTracksTriedHostsAcrossRetries(t *testing.T) {
	zeroReceiptTimeout(t)
	allowSpeculativeAttemptsOnWholeGroup(t)
	env := setupTestProxy(t, 4, nil, true)

	// Kill the primary (host 1) and its natural secondary (host 2).
	// The picker should retry on hosts not yet tried.
	env.killables[1].Kill()
	env.killables[2].Kill()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
	require.NoError(t, err, "host 3 should accept and produce a result")

	requests := env.proxy.perf.RecentRequests()
	require.NotEmpty(t, requests)
	last := requests[len(requests)-1]

	// Each host index must appear at most once in the attempt list.
	seen := map[int]bool{}
	for _, h := range last.Hosts {
		require.False(t, seen[h.HostIdx],
			"picker dispatched host %d more than once for the same request — exclude set was not honoured",
			h.HostIdx)
		seen[h.HostIdx] = true
	}
}

// Test flow:
//  1. Read the speculative attempt cap the package starts with, before any test or phase gate sets it.
//  2. Ask a sixteen-host redundancy for its attempt cap.
//  3. Require both to be DefaultMaxSpeculativeAttempts, so an unconfigured gateway races two hosts instead of the whole group.
func TestMaxSpeculativeAttemptsStartsAtTheDefaultOfTwo(t *testing.T) {
	require.Equal(t, 2, DefaultMaxSpeculativeAttempts)
	require.Equal(t, DefaultMaxSpeculativeAttempts, CurrentMaxSpeculativeAttempts())
	require.Equal(t, DefaultMaxSpeculativeAttempts, (&Redundancy{groupSize: 16}).maxAttempts())
}

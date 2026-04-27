package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunInference_PickerExhaustsEscrowOnceEachHostTried is the
// integration test for the exclude-aware picker. It verifies that when
// every host in the escrow has already failed, RunInference stops
// scheduling new attempts (ErrAllHostsExcluded → picker_exhausted log)
// instead of cycling back to a host already tried.
//
// Setup: 3 hosts, all killed. The maxSpeculativeAttempts cap is bumped
// to 6 (above the 3-host group size). Without the picker's per-request
// exclude memory, the runner would dispatch attempts 4-6 to hosts 1, 2,
// 0 (the natural nonce cycle) — re-trying every dead host a second
// time. With the picker, the runner stops at 3 distinct attempts and
// the request fails fast.
//
// We can't use PerfTracker.RecentRequests() because failed requests
// are not recorded. Instead we check session.Nonce(): each inflight
// consumes one nonce and each timeout settlement consumes one more, so
// the upper bound with the picker is 2 * groupSize = 6. Without the
// picker (running 6 attempts) it would be 12.
func TestRunInference_PickerExhaustsEscrowOnceEachHostTried(t *testing.T) {
	zeroReceiptTimeout(t)
	saved := CurrentMaxSpeculativeAttempts()
	SetMaxSpeculativeAttempts(6)
	t.Cleanup(func() { SetMaxSpeculativeAttempts(saved) })

	env := setupTestProxy(t, 3, nil, true)
	for _, k := range env.killables {
		k.Kill()
	}

	noncesBefore := env.session.Nonce()
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
	require.Error(t, err, "all hosts killed → request must fail")

	advanced := env.session.Nonce() - noncesBefore
	groupSize := uint64(env.proxy.redundancy.groupSize)

	// Picker bound: at most groupSize inflights + groupSize timeout
	// settlements = 2 * groupSize. Anything above means the picker
	// dispatched a duplicate attempt.
	require.LessOrEqual(t, advanced, 2*groupSize,
		"picker should stop after groupSize unique hosts; advanced=%d > 2*groupSize=%d",
		advanced, 2*groupSize)
}

// TestRunInference_PickerTracksTriedHostsAcrossRetries verifies the
// retry-set is being threaded correctly by examining PerfTracker's
// recorded host list. After a failed primary, the secondary must land
// on a different host, and across the entire request no host index
// should appear twice in the attempt list.
func TestRunInference_PickerTracksTriedHostsAcrossRetries(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupTestProxy(t, 4, nil, true)

	// Kill the primary (host 1) and its natural secondary (host 2).
	// The picker should retry on hosts not yet tried.
	env.killables[1].Kill()
	env.killables[2].Kill()

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf)
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

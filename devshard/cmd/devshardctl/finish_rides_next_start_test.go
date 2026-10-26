package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// An honest request leaves its receipt and Finish queued: the next Start diff
// carries them, so a request costs only the nonces of its attempts. The gateway
// keeps a timeout on each attempt's nonce until its Finish is sequenced, and the
// timeout must not publish the queued Finish on a nonce of its own while its
// deadline is ahead.
//
// Test flow:
//  1. A gateway whose escrow has timeouts armed serves one request in full.
//  2. Watch the nonce while no other request arrives.
//  3. Serve a second request and watch the nonce again.
//  4. Check every diff of the two requests opens an inference, and the first
//     request's inferences are finished by the second request's diffs.
func TestRunInference_FinishRidesTheNextStart(t *testing.T) {
	zeroReceiptTimeout(t)
	env := setupManualFloorProxy(t, 3, nil, true)
	ctx := context.Background()
	idle := func(msg string) uint64 {
		t.Helper()
		at := env.session.Nonce()
		require.Never(t, func() bool { return env.session.Nonce() != at }, 500*time.Millisecond, 10*time.Millisecond, msg)
		return at
	}

	seed := env.session.Nonce()
	require.NoError(t, env.proxy.redundancy.RunInference(ctx, defaultParams(), io.Discard, nil))
	first := idle("the queued Finish of an honest request must wait for the next Start, not take a nonce of its own")
	require.Greater(t, first, seed)
	require.NoError(t, env.proxy.redundancy.RunInference(ctx, defaultParams(), io.Discard, nil))
	second := idle("the second request's Finish must wait too")
	require.Greater(t, second, first)

	for _, d := range env.session.Diffs() {
		if d.Nonce <= seed || d.Nonce > second {
			continue
		}
		start := false
		for _, tx := range d.Txs {
			if tx.GetStartInference() != nil {
				start = true
			}
		}
		require.True(t, start, "diff %d opens no inference", d.Nonce)
	}
	for nonce := seed + 1; nonce <= first; nonce++ {
		rec, ok := env.sm.Inference(nonce)
		require.True(t, ok)
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d", nonce)
	}
}

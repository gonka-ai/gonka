package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. A chain serving heights 10 to 20.
//  2. Ask the clock for height 15 twice, for height 0, and for height 30.
//  3. Check 15 is its block's time and asked of the chain once, 0 (no anchor) is no time without a
//     read, 30 is an error, and a nil clock dates nothing.
func TestHeaderClockDatesEachHeightOnce(t *testing.T) {
	chain := newUnixChain(t, 10, 20)
	clock := newHeaderClock(chain.URL)
	ctx := context.Background()
	for range 2 {
		at, err := clock.unix(ctx, 15)
		require.NoError(t, err)
		require.Equal(t, int64(15), at)
	}
	at, err := clock.unix(ctx, 0)
	require.NoError(t, err)
	require.Zero(t, at)
	require.EqualValues(t, 1, chain.headerReads.Load())
	_, err = clock.unix(ctx, 30)
	require.Error(t, err)

	var none *headerClock
	at, err = none.unix(ctx, 15)
	require.NoError(t, err)
	require.Zero(t, at)
}

// Test flow:
//  1. A Gateway whose escrow holds a Started inference anchored at the block of 90 s ago, a Pending
//     one at the block of 30 s ago and a finished one, its records naming heights as v7 does; a
//     chain whose block at height h is at unix second h.
//  2. Collect the pending samples twice.
//  3. Check both open inferences are dated by their blocks: the open starts, the oldest and its age,
//     and each height asked of the chain once over both samples.
func TestCollectPendingDatesStartHeightsByHeaderTime(t *testing.T) {
	now := time.Now().Unix()
	t90, t30 := now-90, now-30
	chain := newUnixChain(t, 1, now)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/devshards":
			_, _ = w.Write([]byte(`{"devshards":[{"id":"11","active":true,"runtime":{"nonce":9}}]}`))
		case "/devshard/11/v1/debug/pending":
			_, _ = w.Write([]byte(`{"nonce":9,"pending":[]}`))
		case "/devshard/11/v1/debug/inferences":
			body, _ := json.Marshal(map[string]any{"inferences": map[string]any{
				"3": map[string]any{"status": "started", "started_at_height": t90, "confirmed_at_height": t90 + 1},
				"5": map[string]any{"status": "pending", "started_at_height": t30},
				"6": map[string]any{"status": "finished", "started_at_height": 1},
			}})
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()

	collect := collectPending(gateway.URL, "key", newHeaderClock(chain.URL))
	var pending []PendingSample
	for range 2 {
		pending = collect(context.Background(), "")
	}
	require.Len(t, pending, 1)
	require.Empty(t, pending[0].Error)
	require.Equal(t, 2, pending[0].Open)
	require.Equal(t, map[string]int64{"3": t90, "5": t30}, pending[0].OpenStarted)
	require.Equal(t, "3", pending[0].OldestInference)
	require.InDelta(t, 90, pending[0].MaxAgeSeconds, 2)
	require.EqualValues(t, 3, chain.headerReads.Load(), "heights 90 s ago, 30 s ago and 1, each once")
}

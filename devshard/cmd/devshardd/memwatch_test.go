package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/memwatch"
)

// TestStatsMemoryJSONFields keeps the /stats/memory names fixed. The process
// fields and the session fields are one flat object.
func TestStatsMemoryJSONFields(t *testing.T) {
	l := memwatch.New(memwatch.Config{
		Inventory: func() memwatch.Inventory {
			return hostInventory{
				Sessions:       2,
				LiveInferences: 7,
				FattestEscrow:  "escrow-1",
				FattestLive:    7,
			}
		},
	})
	l.Sample()

	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats/memory", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	for _, key := range []string{
		"sampled_at",
		"heap_alloc",
		"heap_inuse",
		"heap_idle",
		"heap_released",
		"heap_objects",
		"stack_inuse",
		"sys",
		"goroutines",
		"num_gc",
		"sessions",
		"live_inferences",
		"sealed_inferences",
		"mempool",
		"executing",
		"validating",
		"fattest_escrow",
		"fattest_live",
		"heap",
	} {
		_, ok := raw[key]
		require.Truef(t, ok, "missing %s in %s", key, rec.Body.Bytes())
	}

	var got hostInventory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 2, got.Sessions)
	require.Equal(t, 7, got.LiveInferences)
	require.Equal(t, "escrow-1", got.FattestEscrow)
	require.Equal(t, 7, got.FattestLive)
}

package testutil

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// WaitGatewayHeightSeedReady polls GET /v1/status until height_seed.state is
// ok (or omitted, which means the gate is off). Chat 503s until then.
func WaitGatewayHeightSeedReady(t *testing.T, gatewayURL string, timeout time.Duration) {
	t.Helper()
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = GetJSON(t, client, gatewayURL+"/v1/status")
		if gatewayStatusHeightSeedReady(last) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.Fail(t, "gateway height seed not ready", "last /v1/status: %v", last)
}

func gatewayStatusHeightSeedReady(status map[string]any) bool {
	if status == nil {
		return true
	}
	if !heightSeedValueReady(status["height_seed"]) {
		return false
	}
	devshards, ok := status["devshards"].([]any)
	if !ok {
		return true
	}
	for _, raw := range devshards {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if !heightSeedValueReady(item["height_seed"]) {
			return false
		}
	}
	return true
}

func heightSeedValueReady(v any) bool {
	if v == nil {
		return true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return true
	}
	state, _ := m["state"].(string)
	switch state {
	case "", "ok":
		return true
	default:
		return false
	}
}

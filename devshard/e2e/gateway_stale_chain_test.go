package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// staleSnapshotLimitSeconds sits above the observer's refresh cadence.
const staleSnapshotLimitSeconds = 40

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Set a chain snapshot age limit well above the observer's own refresh cadence.
//  3. Stop the mock chain so the observer has nothing left to refresh from.
//  4. Assert a completion is refused 503 once the snapshot passes the limit.
//  5. Switch the limit off and assert completions are served again.
func TestE2E_GatewayRefusesToServeUnderAStaleChainSnapshot(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	putSettings(t, client, env.clientURL, map[string]any{"chain_snapshot_max_age_seconds": staleSnapshotLimitSeconds})
	env.stopMockChain(context.Background(), t)

	var refused testutil.RawResponse
	require.Eventually(t, func() bool {
		response, err := testutil.SendCompletionRawE(client, env.clientURL, "stale snapshot", testutil.AdminAPIKey)
		if err != nil {
			return false
		}
		refused = response
		return refused.StatusCode == http.StatusServiceUnavailable
	}, 3*staleSnapshotLimitSeconds*time.Second, time.Second,
		"a completion under a snapshot nobody refreshed was never refused")
	if !strings.Contains(refused.Body, "chain snapshot is") {
		t.Errorf("the 503 does not name the stale snapshot: %s", refused.Body)
	}
	if retry := refused.Header.Get("Retry-After"); retry == "" {
		t.Errorf("a 503 for a stale snapshot carries no Retry-After")
	}

	putSettings(t, client, env.clientURL, map[string]any{"chain_snapshot_max_age_seconds": 0})

	served := testutil.SendCompletionRaw(t, client, env.clientURL, "limit switched off", testutil.AdminAPIKey)
	if served.StatusCode != http.StatusOK {
		t.Fatalf("a completion with the age limit switched off = %d %s, want 200", served.StatusCode, served.Body)
	}
}

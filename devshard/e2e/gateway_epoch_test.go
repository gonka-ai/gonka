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

// advanceEpoch drives the chain through a real epoch transition, blocks and all, so the gateway sees the
// rollover the way production delivers it rather than a patched field.
func advanceEpoch(ctx context.Context, t *testing.T, env *e2eEnv) {
	t.Helper()
	host, err := env.mockChain.Host(ctx)
	require.NoError(t, err)
	port, err := env.mockChain.MappedPort(ctx, "9191/tcp")
	require.NoError(t, err)
	advanced := testutil.PostJSONRaw(t, &http.Client{Timeout: testutil.DefaultRequestTimeout},
		"http://"+host+":"+port.Port()+"/testenv/epoch", map[string]any{"advance": true}, "")
	if advanced.StatusCode != http.StatusOK {
		t.Fatalf("advancing the chain's epoch = %d %s", advanced.StatusCode, advanced.Body)
	}
	t.Logf("chain advanced: %s", advanced.Body)
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Serve one completion and read the epoch index off the scrape.
//  3. Drive the chain through a real epoch transition.
//  4. Poll the scrape until the gateway reports the new epoch.
//  5. Assert the gateway still serves and raised no findings.
func TestE2E_GatewayFollowsTheChainAcrossAnEpochBoundary(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "before the epoch", testutil.AdminAPIKey))
	before := epochIndexFromScrape(t, gatewayScrape(t, client, env.clientURL))

	advanceEpoch(ctx, t, env)

	deadline := time.Now().Add(90 * time.Second)
	for {
		if epochIndexFromScrape(t, gatewayScrape(t, client, env.clientURL)) != before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway still reports epoch %s ninety seconds after the chain moved on", before)
		}
		time.Sleep(2 * time.Second)
	}

	awaitServed(t, client, env.clientURL, 60*time.Second)
	if codes := gatewayFindings(t, client, env.statsURL); len(codes) > 0 {
		t.Errorf("crossing an epoch boundary raised findings %v, want none", codes)
	}
}

func epochIndexFromScrape(t *testing.T, scrape string) string {
	t.Helper()
	for line := range strings.SplitSeq(scrape, "\n") {
		if after, found := strings.CutPrefix(line, "devshard_gateway_chain_epoch_index "); found {
			return strings.TrimSpace(after)
		}
	}
	t.Fatal("the scrape carries no devshard_gateway_chain_epoch_index")
	return ""
}

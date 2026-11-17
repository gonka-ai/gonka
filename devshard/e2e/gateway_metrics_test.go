package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"devshard/e2e/testutil"
)

// requireMetrics matches on the TYPE line: a histogram never appears under its bare name, only as _bucket/_sum/_count.
func requireMetrics(t *testing.T, scrape string, families []string) {
	t.Helper()
	for _, family := range families {
		if !strings.Contains(scrape, "# TYPE "+family+" ") {
			t.Errorf("the scrape publishes no %s, so nothing downstream can alert on it", family)
		}
	}
}

func gatewayScrape(t *testing.T, client *http.Client, clientURL string) string {
	t.Helper()
	status, scrape := gatewayGet(t, client, clientURL+"/metrics", "")
	if status != http.StatusOK {
		t.Fatalf("/metrics = %d", status)
	}
	return scrape
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Send buffered, streamed and repeated completions so every ordinary path is exercised.
//  3. Assert the scrape publishes every family a dashboard reads.
func TestE2E_GatewayPublishesEveryMetricOrdinaryTrafficProduces(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "metric traffic", testutil.AdminAPIKey))
	testutil.RequireOpenAIStream(t, testutil.SendStreamingCompletion(t, client, env.clientURL, "metric stream"))
	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "metric traffic", testutil.AdminAPIKey))

	requireMetrics(t, gatewayScrape(t, client, env.clientURL), []string{
		"devshard_http_requests_total",
		"devshard_http_request_duration_seconds",
		"devshard_gateway_requests_total",
		"devshard_gateway_attempts_started_total",
		"devshard_gateway_attempts_terminal_total",
		"devshard_gateway_participant_receipt_seconds",
		"devshard_gateway_participant_total_attempt_seconds",
		"devshard_gateway_inflight_requests",
		"devshard_gateway_cache_hits_total",
		"devshard_gateway_cache_misses_total",
		"devshard_gateway_cache_entries",
		"devshard_gateway_buffered_response_bytes",
		"devshard_gateway_effective_max_concurrent_requests",
		"devshard_gateway_chain_block_height",
		"devshard_gateway_chain_epoch_index",
		"devshard_gateway_chain_snapshot_healthy",
		"devshard_gateway_escrow_weight",
		"devshard_gateway_participants_tracked",
	})
}

// Test flow:
//  1. Start the three-host environment with hosts that fail.
//  2. Drive enough traffic for the failure paths to be taken.
//  3. Assert the scrape publishes the families that exist only once something goes wrong.
func TestE2E_GatewayPublishesTheMetricsOnlyFailuresProduce(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		hostEnvOverrides: map[int]map[string]string{1: brokenHost("502", "upstream is down")},
	})

	for request := range 6 {
		testutil.SendCompletionRaw(t, client, env.clientURL,
			fmt.Sprintf("failing traffic %d", request), testutil.AdminAPIKey)
	}

	requireMetrics(t, gatewayScrape(t, client, env.clientURL), []string{
		"devshard_gateway_attempt_failures_total",
		"devshard_gateway_participant_transport_errors_total",
		"devshard_gateway_participant_breaker_state",
	})
}

// Test flow:
//  1. Start the three-host environment with a cap low enough to shed load.
//  2. Fire concurrent completions until the limiter turns some away.
//  3. Assert the scrape publishes the limiter families.
func TestE2E_GatewayPublishesTheMetricsTheLimiterProduces(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{gatewayEnvOverrides: map[string]string{
		"GATEWAY_MAX_CONCURRENT_REQUESTS":  "1",
		"GATEWAY_ADMISSION_QUEUE_PER_SLOT": "0",
		"GATEWAY_ADMISSION_QUEUE_WAIT_MS":  "0",
	}})

	var pending sync.WaitGroup
	for attempt := range 8 {
		pending.Add(1)
		go func() {
			defer pending.Done()
			_, _ = testutil.SendCompletionRawE(client, env.clientURL,
				fmt.Sprintf("limited %d", attempt), testutil.AdminAPIKey)
		}()
	}
	pending.Wait()

	requireMetrics(t, gatewayScrape(t, client, env.clientURL), []string{
		"devshard_gateway_limit_rejections_total",
		"devshard_gateway_effective_max_concurrent_requests",
		"devshard_gateway_inflight_requests_by_model",
	})
}

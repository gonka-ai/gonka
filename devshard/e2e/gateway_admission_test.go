package e2e

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the three-host environment with a cap of one concurrent request and no admission queue.
//  2. Fire eight completions at once.
//  3. Assert at least one was turned away with 429.
//  4. Assert no nonce was taken and then abandoned before dispatch.
func TestE2E_GatewayTurnsAwayAnOverloadBeforeItTakesANonce(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{gatewayEnvOverrides: map[string]string{
		"GATEWAY_MAX_CONCURRENT_REQUESTS":  "1",
		"GATEWAY_ADMISSION_QUEUE_PER_SLOT": "0",
		"GATEWAY_ADMISSION_QUEUE_WAIT_MS":  "0",
	}})

	const attempts = 8
	statuses := make([]int, attempts)
	var pending sync.WaitGroup
	for attempt := range attempts {
		pending.Add(1)
		go func() {
			defer pending.Done()
			response, err := testutil.SendCompletionRawE(client, env.clientURL,
				fmt.Sprintf("overload %d", attempt), testutil.AdminAPIKey)
			if err != nil {
				statuses[attempt] = -1
				return
			}
			statuses[attempt] = response.StatusCode
		}()
	}
	pending.Wait()

	turnedAway := 0
	for _, status := range statuses {
		if status == http.StatusTooManyRequests {
			turnedAway++
		}
	}
	if turnedAway == 0 {
		t.Fatalf("a cap of one concurrent request admitted all %d at once: statuses %v", attempts, statuses)
	}
	if abandoned := gatewayGhostBurns(t, client, env.statsURL, "request_abandoned_before_dispatch"); abandoned > 0 {
		t.Errorf("%d nonces were taken and then abandoned: the gateway reserved before it refused", abandoned)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read the session nonce before any traffic.
//  3. Send one completion naming a model no escrow serves.
//  4. Assert the gateway answers 4xx.
//  5. Assert the session nonce did not move.
func TestE2E_GatewayRejectsAnUnservedModelWithoutSpendingANonce(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	before := gatewaySessionNonce(t, client, env.clientURL)
	body := testutil.ChatCompletionBody("unserved model", false)
	body["model"] = "a-model-no-escrow-serves"
	rejected := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", body, testutil.AdminAPIKey)
	if rejected.StatusCode < 400 || rejected.StatusCode >= 500 {
		t.Fatalf("an unserved model was answered %d %s, want a 4xx", rejected.StatusCode, rejected.Body)
	}
	if after := gatewaySessionNonce(t, client, env.clientURL); after != before {
		t.Errorf("nonce moved %d -> %d on a request no host ever saw", before, after)
	}
}

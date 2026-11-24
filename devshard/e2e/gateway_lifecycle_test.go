package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/e2e/testutil"
)

func gatewayPost(t *testing.T, client *http.Client, url string) testutil.RawResponse {
	t.Helper()
	return testutil.PostRaw(t, client, url, "application/json", nil, testutil.AdminAPIKey)
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Deactivate the escrow and assert a request is no longer served.
//  3. Activate it again, waiting out the drain while it answers "still draining".
//  4. Poll until the escrow is back in service.
func TestE2E_GatewayDeactivatesAnEscrowAndBringsItBack(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "before draining", testutil.AdminAPIKey))

	admin := env.clientURL + "/v1/admin/devshards/" + defaultEscrowID
	if deactivated := gatewayPost(t, client, admin+"/deactivate"); deactivated.StatusCode != http.StatusOK {
		t.Fatalf("deactivate = %d %s", deactivated.StatusCode, deactivated.Body)
	}
	if drained := testutil.SendCompletionRaw(t, client, env.clientURL, "while drained", testutil.AdminAPIKey); drained.StatusCode == http.StatusOK {
		t.Errorf("a deactivated escrow still served a request: %s", drained.Body)
	}

	awaitActivated(t, client, admin, 10*time.Second)
	awaitServed(t, client, env.clientURL, 30*time.Second)
}

func awaitActivated(t *testing.T, client *http.Client, admin string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		activated := gatewayPost(t, client, admin+"/activate")
		if activated.StatusCode == http.StatusOK {
			return
		}
		if activated.StatusCode != http.StatusConflict || !strings.Contains(activated.Body, "still draining") {
			t.Fatalf("activate = %d %s", activated.StatusCode, activated.Body)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the escrow was still draining after %s: activate = %d %s", within, activated.StatusCode, activated.Body)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func awaitServed(t *testing.T, client *http.Client, clientURL string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last testutil.RawResponse
	for time.Now().Before(deadline) {
		last = testutil.SendCompletionRaw(t, client, clientURL, "after draining", testutil.AdminAPIKey)
		if last.StatusCode == http.StatusOK {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the escrow never came back into service within %s: last answer %d %s", within, last.StatusCode, last.Body)
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Settle the escrow, retrying while it answers 409 "devshard busy" because the startup warm-up still holds it.
//  3. Activate it again and assert 404: serving it would spend nonces the settlement missed.
func TestE2E_GatewayCannotActivateAnEscrowItHasSettled(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	admin := env.clientURL + "/v1/admin/devshards/" + defaultEscrowID
	if settled := settleRetryingBusy(t, client, admin+"/settle"); settled.StatusCode != http.StatusOK {
		t.Fatalf("settle = %d %s", settled.StatusCode, settled.Body)
	}
	reactivated := gatewayPost(t, client, admin+"/activate")
	if reactivated.StatusCode != http.StatusNotFound {
		t.Errorf("activating a settled escrow = %d %s, want 404", reactivated.StatusCode, reactivated.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Post every lifecycle action against an escrow id the gateway does not have.
//  3. Assert each one answers 404 rather than acting.
func TestE2E_GatewayRefusesLifecycleOnAnUnknownEscrow(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	for _, action := range []string{"/activate", "/deactivate", "/settle"} {
		response := gatewayPost(t, client, env.clientURL+"/v1/admin/devshards/999999"+action)
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s on an unknown escrow = %d %s, want 404", action, response.StatusCode, response.Body)
		}
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read the escrow listing routes and assert each answers 200.
//  3. Read the escrow's participants and assert it lists one slot per host the stack runs.
func TestE2E_GatewayListsItsEscrowsAndTheirParticipants(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	for _, path := range []string{"/v1/admin/devshards", "/v1/admin/suspicious-hosts"} {
		if status, body := gatewayGet(t, client, env.clientURL+path, testutil.AdminAPIKey); status != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200", path, status, body)
		}
	}

	status, body := gatewayGet(t, client, env.clientURL+"/v1/admin/devshards/"+defaultEscrowID+"/participants", testutil.AdminAPIKey)
	if status != http.StatusOK {
		t.Fatalf("participants = %d %s", status, body)
	}
	var listed map[string]any
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("participants are not JSON: %v (%s)", err, body)
	}
	slots, _ := listed["slots"].([]any)
	if len(slots) != 3 {
		t.Errorf("the escrow lists %d slots, want the three hosts the stack runs: %s", len(slots), body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read each debug route with the admin key and assert 200.
//  3. Read each one anonymously and assert it does not answer.
func TestE2E_GatewayAnswersOnItsDebugRoutes(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	for _, path := range []string{"/v1/debug/rotation", "/v1/debug/memstats"} {
		if status, body := gatewayGet(t, client, env.clientURL+path, testutil.AdminAPIKey); status != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200", path, status, body)
		}
		if status, _ := gatewayGet(t, client, env.clientURL+path, ""); status == http.StatusOK {
			t.Errorf("GET %s answered without the admin key", path)
		}
	}
}

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"devshard/e2e/testutil"
)

func putSettings(t *testing.T, client *http.Client, clientURL string, overrides map[string]any) {
	t.Helper()
	applied := testutil.PostJSONRaw(t, client, clientURL+"/v1/admin/settings", overrides, testutil.AdminAPIKey)
	if applied.StatusCode != http.StatusOK {
		t.Fatalf("applying %v = %d %s", overrides, applied.StatusCode, applied.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Set a cap of one concurrent request through the settings route.
//  3. Fire concurrent completions.
//  4. Assert the new cap turned some of them away, without a restart.
func TestE2E_GatewayAppliesANewLimitWithoutARestart(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	putSettings(t, client, env.clientURL, map[string]any{"max_concurrent_requests": 1, "admission_queue_wait_ms": 0})

	const attempts = 8
	statuses := make([]int, attempts)
	var pending sync.WaitGroup
	for attempt := range attempts {
		pending.Add(1)
		go func() {
			defer pending.Done()
			response, err := testutil.SendCompletionRawE(client, env.clientURL,
				fmt.Sprintf("after reconfigure %d", attempt), testutil.AdminAPIKey)
			if err != nil {
				statuses[attempt] = -1
				return
			}
			statuses[attempt] = response.StatusCode
		}()
	}
	pending.Wait()

	for _, status := range statuses {
		if status == http.StatusTooManyRequests {
			return
		}
	}
	t.Errorf("a cap set through /v1/admin/settings admitted all %d at once: statuses %v", attempts, statuses)
}

// Test flow:
//  1. Start the three-host environment with an API key configured.
//  2. Put the served model behind that key through the settings route.
//  3. Assert an unauthenticated completion is answered 401.
//  4. Assert the same completion with the key is served.
func TestE2E_GatewayLocksAModelBehindAKeyAtRuntime(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{"DEVSHARD_API_KEYS": testutil.AdminAPIKey},
	})

	putSettings(t, client, env.clientURL, map[string]any{"model_access": map[string]string{"stub-model": "api_key"}})

	anonymous := testutil.SendCompletionRaw(t, client, env.clientURL, "no key", "")
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unauthenticated request = %d %s, want 401", anonymous.StatusCode, anonymous.Body)
	}
	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "with key", testutil.AdminAPIKey))
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Apply a setting that would put the model behind a key nobody holds.
//  3. Assert it is refused with 400 as the operator's mistake, not a 502.
//  4. Assert the model is still served.
func TestE2E_GatewayRefusesASettingThatWouldStrandAModel(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	refused := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/settings",
		map[string]any{"model_access": map[string]string{"stub-model": "api_key"}}, testutil.AdminAPIKey)
	if refused.StatusCode != http.StatusBadRequest {
		t.Errorf("stranding a model = %d %s, want 400", refused.StatusCode, refused.Body)
	}
	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "still served", testutil.AdminAPIKey))
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Set an access map naming only some other model.
//  3. Assert the served model, now unlisted, answers 401 to an unauthenticated request.
func TestE2E_GatewayClosesModelsLeftOutOfTheAccessMap(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	putSettings(t, client, env.clientURL, map[string]any{"model_access": map[string]string{"some-other-model": "open"}})

	anonymous := testutil.SendCompletionRaw(t, client, env.clientURL, "unlisted model", "")
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Errorf("a model left out of the access map answered %d %s, want 401", anonymous.StatusCode, anonymous.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Apply a setting through the settings route.
//  3. Read the route back and assert it reports what was applied.
func TestE2E_GatewaySettingsReadBackWhatWasApplied(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	putSettings(t, client, env.clientURL, map[string]any{"max_concurrent_requests": 7})

	status, body := gatewayGet(t, client, env.clientURL+"/v1/admin/settings", testutil.AdminAPIKey)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d %s", status, body)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatalf("settings are not JSON: %v (%s)", err, body)
	}
	if got := testutil.NumericField(t, settings, "max_concurrent_requests"); got != 7 {
		t.Errorf("max_concurrent_requests reads back as %d, want the 7 that was applied", got)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Apply an engine timing through the settings route.
//  3. Read the route back and assert it reports the stored value.
//  4. Apply a grace shorter than the stall it must outlive and assert the route refuses it.
func TestE2E_GatewayEngineTimingsReadBackAndAreValidated(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	putSettings(t, client, env.clientURL, map[string]any{"engine_receipt_timeout_ms": 7000})

	status, body := gatewayGet(t, client, env.clientURL+"/v1/admin/settings", testutil.AdminAPIKey)
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d %s", status, body)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatalf("settings are not JSON: %v (%s)", err, body)
	}
	if got := testutil.NumericField(t, settings, "engine_receipt_timeout_ms"); got != 7000 {
		t.Errorf("engine_receipt_timeout_ms reads back as %d, want the 7000 that was applied", got)
	}

	refused := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/settings",
		map[string]any{"engine_loser_grace_ms": 1000, "engine_inter_chunk_stall_ms": 30000}, testutil.AdminAPIKey)
	if refused.StatusCode != http.StatusBadRequest {
		t.Errorf("a grace shorter than the stall was answered %d %s, want 400", refused.StatusCode, refused.Body)
	}
	if !strings.Contains(refused.Body, "engine_loser_grace_ms") {
		t.Errorf("the refusal does not name the field it refused: %s", refused.Body)
	}
}

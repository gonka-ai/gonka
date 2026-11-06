package e2e

import (
	"net/http"
	"testing"

	"devshard/cmd/gateway/api"
	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Serve one completion and read the request id and escrow off its headers.
//  3. Trace that id and assert the trace names the same request, escrow and model.
func TestE2E_GatewayTracesAServedRequestByItsID(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	served := testutil.SendCompletionRaw(t, client, env.clientURL, "traceable", testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, served)

	requestID := served.Header.Get(api.RequestIDHeader)
	if requestID == "" {
		t.Fatalf("a served request carried no %s header, so nothing can look it up", api.RequestIDHeader)
	}
	if escrow := served.Header.Get(api.EscrowHeader); escrow != defaultEscrowID {
		t.Errorf("%s = %q, want the escrow that served it", api.EscrowHeader, escrow)
	}

	status, body := gatewayGet(t, client, env.clientURL+"/v1/requests/"+requestID, testutil.AdminAPIKey)
	if status != http.StatusOK {
		t.Fatalf("tracing %s = %d %s", requestID, status, body)
	}
	trace := testutil.GetJSON(t, client, env.clientURL+"/v1/requests/"+requestID)
	for field, want := range map[string]string{"request_id": requestID, "escrow_id": defaultEscrowID, "model": "stub-model"} {
		if got, _ := trace[field].(string); got != want {
			t.Errorf("trace %s = %q, want %q", field, got, want)
		}
	}
	if winner, _ := trace["winner_participant"].(string); winner == "" {
		t.Error("the trace names no winner, so it cannot say which host was paid for the answer")
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Trace an id the gateway never issued and assert 404.
func TestE2E_GatewayRefusesToTraceARequestItNeverSaw(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	status, _ := gatewayGet(t, client, env.clientURL+"/v1/requests/req-that-never-existed", testutil.AdminAPIKey)
	if status != http.StatusNotFound {
		t.Errorf("tracing an unknown id = %d, want 404", status)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read the model catalogue and assert it offers the model the stack serves.
//  3. Read the escrow-pinned catalogue and assert it offers one too.
//  4. Read both status routes anonymously and assert they answer.
func TestE2E_GatewayPublishesWhatItServes(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	models := testutil.GetJSON(t, client, env.clientURL+"/v1/models")
	if !modelListNames(t, models)["stub-model"] {
		t.Errorf("/v1/models does not offer the model the stack serves: %v", models)
	}
	pinned := testutil.GetJSON(t, client, env.clientURL+"/devshard/"+defaultEscrowID+"/v1/models")
	if !modelListNames(t, pinned)["stub-model"] {
		t.Errorf("the pinned escrow offers no model: %v", pinned)
	}

	for _, path := range []string{"/v1/status", "/devshard/" + defaultEscrowID + "/v1/status"} {
		if status, body := gatewayGet(t, client, env.clientURL+path, ""); status != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200", path, status, body)
		}
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read a route that does not exist and assert 404.
//  3. Write to a read-only route and assert it refuses rather than acting.
func TestE2E_GatewayRefusesUnknownRoutesAndWrongMethods(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	if status, _ := gatewayGet(t, client, env.clientURL+"/v1/nothing/here", testutil.AdminAPIKey); status != http.StatusNotFound {
		t.Errorf("an unknown route = %d, want 404", status)
	}
	posted := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/models", map[string]any{}, testutil.AdminAPIKey)
	if posted.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/models = %d %s, want 405", posted.StatusCode, posted.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read every recovery route with the admin key and assert each answers.
//  3. Read each one anonymously and assert it does not.
func TestE2E_GatewayAnswersOnEveryRecoveryRoute(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.RequireOpenAINonStreamingCompletion(t,
		testutil.SendCompletionRaw(t, client, env.clientURL, "before recovery", testutil.AdminAPIKey))

	base := env.clientURL + "/devshard/" + defaultEscrowID + "/v1"
	for _, path := range []string{"/state", "/debug/state", "/debug/inferences", "/debug/pending", "/debug/signatures"} {
		if status, body := gatewayGet(t, client, base+path, testutil.AdminAPIKey); status != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200", path, status, body)
		}
		if status, _ := gatewayGet(t, client, base+path, ""); status == http.StatusOK {
			t.Errorf("GET %s answered without the admin key: the recovery surface is not public", path)
		}
	}
}

func modelListNames(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()
	entries, _ := body["data"].([]any)
	names := make(map[string]bool, len(entries))
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := entry["id"].(string); id != "" {
			names[id] = true
		}
	}
	return names
}

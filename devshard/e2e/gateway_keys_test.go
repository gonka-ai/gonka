package e2e

import (
	"net/http"
	"testing"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Create an escrow without naming a key environment variable and assert 400.
//  3. Create an escrow naming one but no model or amount and assert 400.
func TestE2E_GatewayTakesAKeyByEnvNameOrNotAtAll(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	unnamed := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/escrows",
		map[string]any{"model_id": "stub-model", "amount": 1000}, testutil.AdminAPIKey)
	if unnamed.StatusCode != http.StatusBadRequest {
		t.Errorf("creating an escrow without naming a key env = %d %s, want 400", unnamed.StatusCode, unnamed.Body)
	}
	incomplete := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/escrows",
		map[string]any{"private_key_env": "SOME_KEY_ENV"}, testutil.AdminAPIKey)
	if incomplete.StatusCode != http.StatusBadRequest {
		t.Errorf("creating an escrow with no model or amount = %d %s, want 400", incomplete.StatusCode, incomplete.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Unquarantine a participant the gateway has never seen and assert 404.
//  3. Unquarantine nobody at all and assert 400.
func TestE2E_GatewayRefusesToUnquarantineAParticipantItDoesNotKnow(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	unknown := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/participants/unquarantine",
		map[string]any{"participant_key": "gonka1nobody"}, testutil.AdminAPIKey)
	if unknown.StatusCode != http.StatusNotFound {
		t.Errorf("unquarantining an unknown participant = %d %s, want 404", unknown.StatusCode, unknown.Body)
	}
	blank := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/participants/unquarantine",
		map[string]any{}, testutil.AdminAPIKey)
	if blank.StatusCode != http.StatusBadRequest {
		t.Errorf("unquarantining nobody = %d %s, want 400", blank.StatusCode, blank.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Reset accounting for epoch 1 and assert 200.
//  3. Reset accounting for epoch 0, which means unconstrained inside the ledger, and assert it is refused.
func TestE2E_GatewayRefusesToResetTheUnconstrainedEpoch(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	if reset := gatewayPost(t, client, env.clientURL+"/v1/admin/accounting/reset/1"); reset.StatusCode != http.StatusOK {
		t.Errorf("resetting epoch 1 = %d %s, want 200", reset.StatusCode, reset.Body)
	}
	zero := gatewayPost(t, client, env.clientURL+"/v1/admin/accounting/reset/0")
	if zero.StatusCode == http.StatusOK {
		t.Errorf("resetting epoch 0 was accepted, which would clear every epoch: %s", zero.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Import an escrow without naming a source path and assert 400.
func TestE2E_GatewayRefusesAnImportWithNothingToImport(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	empty := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/admin/devshards/import",
		map[string]any{"id": defaultEscrowID}, testutil.AdminAPIKey)
	if empty.StatusCode != http.StatusBadRequest {
		t.Errorf("importing with no source path = %d %s, want 400", empty.StatusCode, empty.Body)
	}
}

// Test flow:
//  1. Start the default three-host gateway environment.
//  2. Read the finalize route with the admin key and assert it answers rather than failing; 409 is an answer.
//  3. Read it anonymously and assert it does not answer.
func TestE2E_GatewayAnswersOnFinalize(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	finalize := env.clientURL + "/devshard/" + defaultEscrowID + "/v1/finalize"
	if status, body := gatewayGet(t, client, finalize, testutil.AdminAPIKey); status >= http.StatusInternalServerError {
		t.Errorf("GET finalize = %d %s, want an answer rather than a failure", status, body)
	}
	if status, _ := gatewayGet(t, client, finalize, ""); status == http.StatusOK {
		t.Error("finalize answered without the admin key")
	}
}

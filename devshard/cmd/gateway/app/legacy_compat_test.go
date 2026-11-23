package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/store"
)

const (
	legacyUpgradedDocument = `{"model_access":{"model-a":"open","model-b":"api_key"},"model_limits":{"model-a":{"default_max_tokens":111,"max_tokens_cap":222,"max_model_len":3333}}}`
	otherChatBody          = `{"model":"model-b","messages":[{"role":"user","content":"hi"}]}`
)

func (g keyedGateway) chatStatusFor(body, key string) int {
	g.t.Helper()
	status, _ := g.call(http.MethodPost, "/v1/chat/completions", body, key)
	return status
}

func warnings(logged *logcapture.Recorder) string {
	var messages []string
	for _, entry := range logged.All() {
		if entry.Level == "warn" {
			messages = append(messages, entry.Msg)
		}
	}
	return strings.Join(messages, "\n")
}

func storeLegacyDocument(t *testing.T, document string) {
	t.Helper()
	records, err := store.Open(os.Getenv("DEVSHARD_STORAGE_DIR"))
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	overrides, err := config.ParseStoredOverrides([]byte(document))
	if err != nil {
		t.Fatalf("ParseStoredOverrides(): %v", err)
	}
	if err := records.SaveOverrides(context.Background(), overrides); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

// Test flow:
//  1. Save into a store a document an earlier build wrote, holding `model_access` and `model_limits`, and set `DEVSHARD_API_KEYS`, with the log captured.
//  2. Compose a gateway over that store.
//  3. Assert the effective limits and access of the model equal the stored ones, a keyless request to the open model is not refused and one to the `api_key` model is 401 without a key and served with the environment key.
//  4. Assert the settings route returns the document as stored, without a `models` block.
//  5. Assert boot logged one warning for each legacy settings key and one for the environment variable.
func TestAnUpgradedStoreWithLegacySettingsBootsAndServesWithTheSameLimitsAndAccess(t *testing.T) {
	logged := logcapture.Install(t)
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)
	t.Setenv("DEVSHARD_API_KEYS", firstFakeClientKey)
	storeLegacyDocument(t, legacyUpgradedDocument)

	composed := composedGateway(t)
	gateway := keyedGateway{t: t, handler: composed.server.Handler}

	limits := composed.config.Load().For(keyedModel).Limits
	if limits.DefaultMaxTokens != 111 || limits.MaxTokensCap != 222 || limits.Access != config.ModelAccessOpen || limits.MaxModelLen == nil || *limits.MaxModelLen != 3333 {
		t.Fatalf("effective limits = %+v, want the stored limits and the open access", limits)
	}
	if access := composed.config.Load().For("model-b").Limits.Access; access != config.ModelAccessAPIKey {
		t.Fatalf("model-b access = %s, want api_key", access)
	}
	if status := gateway.chatStatus(""); status == http.StatusUnauthorized {
		t.Fatalf("a keyless request to the open model = %d, want it accepted", status)
	}
	if status := gateway.chatStatusFor(otherChatBody, ""); status != http.StatusUnauthorized {
		t.Fatalf("a keyless request to the api_key model = %d, want 401", status)
	}
	if status := gateway.chatStatusFor(otherChatBody, firstFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the environment key on the api_key model = %d, want it accepted", status)
	}

	_, stored := gateway.admin(http.MethodGet, "/v1/admin/settings", "")
	if !strings.Contains(stored, `"model_access"`) || !strings.Contains(stored, `"model_limits"`) || strings.Contains(stored, `"models"`) {
		t.Fatalf("settings = %s, want the stored document with its legacy keys and no models block", stored)
	}

	written := warnings(logged)
	for _, want := range []string{"settings key model_limits is deprecated", "settings key model_access is deprecated", "DEVSHARD_API_KEYS is deprecated"} {
		if strings.Count(written, want) != 1 {
			t.Fatalf("warnings = %q, want %q once", written, want)
		}
	}
}

// Test flow:
//  1. Compose a gateway whose models are all admin-only, and assert a keyless request is 401.
//  2. Post `model_access` opening one model, with the log captured, and assert 200.
//  3. Assert the next keyless request to that model is not refused, the stored document holds `model_access`, and one warning names `model_access`.
//  4. Post an unrelated key and assert the stored `model_access` stays and no further warning is logged, then put a document carrying `model_limits` and assert a warning names it.
func TestAPostWithModelAccessTakesEffect(t *testing.T) {
	logged := logcapture.Install(t)
	gateway := composedKeyedGateway(t)
	if status := gateway.chatStatus(""); status != http.StatusUnauthorized {
		t.Fatalf("a keyless request before the write = %d, want 401", status)
	}

	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", `{"model_access":{"model-a":"open"}}`)

	if status := gateway.chatStatus(""); status == http.StatusUnauthorized {
		t.Fatalf("a keyless request after model_access opened the model = %d, want it accepted", status)
	}
	if got := strings.Count(warnings(logged), "settings key model_access is deprecated"); got != 1 {
		t.Fatalf("model_access warnings = %d, want 1", got)
	}
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", `{"disabled":false}`)
	_, stored := gateway.admin(http.MethodGet, "/v1/admin/settings", "")
	if !strings.Contains(stored, `"model_access":{"model-a":"open"}`) {
		t.Fatalf("settings = %s, want model_access kept by an unrelated write", stored)
	}
	if got := strings.Count(warnings(logged), "settings key model_access is deprecated"); got != 1 {
		t.Fatalf("model_access warnings after an unrelated write = %d, want still 1", got)
	}
	gateway.requireAdminStatus(http.StatusOK, http.MethodPut, "/v1/admin/settings", `{"model_limits":{"model-a":{"default_max_tokens":10,"max_tokens_cap":20}}}`)
	if got := strings.Count(warnings(logged), "settings key model_limits is deprecated"); got != 1 {
		t.Fatalf("model_limits warnings after a put carrying it = %d, want 1", got)
	}
}

type listedAPIKey struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

func listAPIKeys(t *testing.T, gateway keyedGateway) ([]listedAPIKey, string) {
	t.Helper()
	_, listing := gateway.admin(http.MethodGet, "/v1/admin/api-keys", "")
	var decoded struct {
		APIKeys []listedAPIKey `json:"api_keys"`
	}
	if err := json.Unmarshal([]byte(listing), &decoded); err != nil {
		t.Fatalf("list = %s (%v)", listing, err)
	}
	return decoded.APIKeys, listing
}

// Test flow:
//  1. Compose a gateway with `DEVSHARD_API_KEYS` holding one key, the model on the `api_key` tier, and one key created through the admin API.
//  2. Assert the environment key authenticates, and the list shows it with source `env` and the created key with source `database`, and never a key value.
//  3. Assert reading the environment key by ID gives no value, deleting it answers 409 naming the variable, and creating the same key answers 409.
//  4. Revoke the created key and assert 200: the environment key counts as an active key for the last-key rule, and it still authenticates.
func TestEnvironmentClientKeysAuthenticateAreListedAndCannotBeRevokedOrRecreated(t *testing.T) {
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)
	t.Setenv("DEVSHARD_API_KEYS", firstFakeClientKey)
	composed := composedGateway(t)
	gateway := keyedGateway{t: t, handler: composed.server.Handler}
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", keyedModelSettings)
	gateway.createKey(fmt.Sprintf(`{"key":%q}`, secondFakeClientKey))

	if status := gateway.chatStatus(firstFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the environment key = %d, want it accepted", status)
	}
	keys, listing := listAPIKeys(t, gateway)
	want := []listedAPIKey{{ID: digestPrefix(secondFakeClientKey), Source: "database"}, {ID: digestPrefix(firstFakeClientKey), Source: "env"}}
	if len(keys) != 2 || keys[0] != want[0] || keys[1] != want[1] || strings.Contains(listing, firstFakeClientKey) {
		t.Fatalf("list = %s, want %+v and no key value", listing, want)
	}

	environmentPath := "/v1/admin/api-keys/" + digestPrefix(firstFakeClientKey)
	read := gateway.requireAdminStatus(http.StatusOK, http.MethodGet, environmentPath, "")
	if _, carriesKey := read["key"]; carriesKey || read["source"] != "env" {
		t.Fatalf("read of the environment key = %v, want source env and no value", read)
	}
	status, response := gateway.admin(http.MethodDelete, environmentPath, "")
	if status != http.StatusConflict || !strings.Contains(response, "DEVSHARD_API_KEYS") {
		t.Fatalf("delete of the environment key = %d %s, want 409 naming DEVSHARD_API_KEYS", status, response)
	}
	status, response = gateway.admin(http.MethodPost, "/v1/admin/api-keys", fmt.Sprintf(`{"key":%q}`, firstFakeClientKey))
	if status != http.StatusConflict || strings.Contains(response, firstFakeClientKey) {
		t.Fatalf("create of the environment key = %d %s, want 409 without the value", status, response)
	}

	gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, "/v1/admin/api-keys/"+digestPrefix(secondFakeClientKey), "")
	if status := gateway.chatStatus(firstFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the environment key after the database key was revoked = %d, want it accepted", status)
	}
}

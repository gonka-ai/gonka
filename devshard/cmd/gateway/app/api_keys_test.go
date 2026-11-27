package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/store"
)

const (
	firstFakeClientKey  = "fake-client-key-alpha-0001"
	secondFakeClientKey = "fake-client-key-bravo-0002"
	keyedModel          = "model-a"
	keyedChatBody       = `{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`
	keyedModelSettings  = `{"models":{"model-a":{"access":"api_key"}}}`
)

func apiKeyModelOverrides() config.Overrides {
	access := config.ModelAccessAPIKey
	return config.Overrides{Models: map[string]config.ModelSettings{keyedModel: {Access: &access}}}
}

type keyedGateway struct {
	t       *testing.T
	handler http.Handler
}

func composedKeyedGateway(t *testing.T) keyedGateway {
	t.Helper()
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)
	return keyedGateway{t: t, handler: composedGateway(t).server.Handler}
}

func (g keyedGateway) call(method, path, body, bearer string) (int, string) {
	g.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	recorder := httptest.NewRecorder()
	g.handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func (g keyedGateway) admin(method, path, body string) (int, string) {
	g.t.Helper()
	return g.call(method, path, body, routeTestAdminKey)
}

func (g keyedGateway) chatStatus(key string) int {
	g.t.Helper()
	status, _ := g.call(http.MethodPost, "/v1/chat/completions", keyedChatBody, key)
	return status
}

func (g keyedGateway) requireAdminStatus(want int, method, path, body string) map[string]any {
	g.t.Helper()
	status, response := g.admin(method, path, body)
	if status != want {
		g.t.Fatalf("%s %s = %d %s, want %d", method, path, status, response, want)
	}
	var decoded map[string]any
	_ = json.Unmarshal([]byte(response), &decoded)
	return decoded
}

func (g keyedGateway) createKey(body string) map[string]any {
	g.t.Helper()
	return g.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/api-keys", body)
}

func digestPrefix(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])[:8]
}

// Test flow:
//  1. Compose a gateway with an admin key and no client keys, and create a key through the admin API with a label.
//  2. Put the model on the `api_key` tier through the settings route.
//  3. Assert the very next chat request with the new key is not refused, and one with an unknown key is 401.
//  4. Create a second key, revoke the first, and assert the very next request with the first key is 401 while the second is served.
func TestACreatedKeyServesTheNextRequestAndARevokedOneDoesNot(t *testing.T) {
	gateway := composedKeyedGateway(t)

	created := gateway.createKey(`{"label":"alpha"}`)
	generated, _ := created["key"].(string)
	if !strings.HasPrefix(generated, "sk-") || len(generated) < 16 {
		t.Fatalf("created key = %q, want a generated key", generated)
	}
	if created["id"] != digestPrefix(generated) {
		t.Fatalf("created id = %v, want the key's digest prefix %s", created["id"], digestPrefix(generated))
	}
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", keyedModelSettings)

	if status := gateway.chatStatus(generated); status == http.StatusUnauthorized {
		t.Fatalf("a freshly created key = %d, want it accepted on the next request", status)
	}
	if status := gateway.chatStatus("fake-unknown-key-0000000"); status != http.StatusUnauthorized {
		t.Fatalf("an unknown key = %d, want 401", status)
	}

	gateway.createKey(fmt.Sprintf(`{"key":%q}`, secondFakeClientKey))
	gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, "/v1/admin/api-keys/"+digestPrefix(generated), "")

	if status := gateway.chatStatus(generated); status != http.StatusUnauthorized {
		t.Fatalf("a revoked key = %d, want 401 on the next request", status)
	}
	if status := gateway.chatStatus(secondFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the surviving key = %d, want it still accepted", status)
	}
}

// Test flow:
//  1. Compose a gateway, create the key with the larger ID first and the other second, and revoke the first, with the log captured.
//  2. List the keys and assert the answer names both IDs, labels and the revoked state in creation order, and holds neither key value.
//  3. Read each key by ID and assert the answer carries its value.
//  4. Assert the audit lines for create, read and revoke carry the ID and label and never a key value.
//  5. Assert a caller without the admin key is refused on every route.
func TestTheListNeverCarriesAKeyAndReadByIDDoes(t *testing.T) {
	logged := logcapture.Install(t)
	gateway := composedKeyedGateway(t)
	gateway.createKey(fmt.Sprintf(`{"key":%q,"label":"bravo"}`, secondFakeClientKey))
	gateway.createKey(fmt.Sprintf(`{"key":%q,"label":"alpha"}`, firstFakeClientKey))
	gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, "/v1/admin/api-keys/"+digestPrefix(secondFakeClientKey), "")

	_, listing := gateway.admin(http.MethodGet, "/v1/admin/api-keys", "")
	for _, key := range []string{firstFakeClientKey, secondFakeClientKey} {
		if strings.Contains(listing, key) {
			t.Fatalf("the list carries a key value: %s", listing)
		}
	}
	var decoded struct {
		APIKeys []struct {
			ID      string `json:"id"`
			Label   string `json:"label"`
			Revoked bool   `json:"revoked"`
		} `json:"api_keys"`
	}
	if err := json.Unmarshal([]byte(listing), &decoded); err != nil || len(decoded.APIKeys) != 2 {
		t.Fatalf("list = %s (%v), want two keys", listing, err)
	}
	if first, second := decoded.APIKeys[0], decoded.APIKeys[1]; first.ID != digestPrefix(secondFakeClientKey) || first.Label != "bravo" || !first.Revoked ||
		second.ID != digestPrefix(firstFakeClientKey) || second.Label != "alpha" || second.Revoked {
		t.Fatalf("list entries = %+v, want bravo revoked first and alpha active second", decoded.APIKeys)
	}

	for _, key := range []string{firstFakeClientKey, secondFakeClientKey} {
		read := gateway.requireAdminStatus(http.StatusOK, http.MethodGet, "/v1/admin/api-keys/"+digestPrefix(key), "")
		if read["key"] != key {
			t.Fatalf("read by ID = %v, want the key value", read)
		}
	}

	for message, label := range map[string]string{"admin: api key created": "bravo", "admin: api key read": "alpha", "admin: api key revoked": "bravo"} {
		entry, found := logged.Find(message)
		if !found {
			t.Fatalf("no %q audit line", message)
		}
		wantID := digestPrefix(firstFakeClientKey)
		if label == "bravo" {
			wantID = digestPrefix(secondFakeClientKey)
		}
		if logcapture.Field(entry, "api_key_id") != wantID || logcapture.Field(entry, "api_key_label") != label {
			t.Fatalf("%q line = %+v, want id %s and label %s", message, entry.Fields, wantID, label)
		}
	}
	for _, entry := range logged.All() {
		if strings.Contains(fmt.Sprint(entry), firstFakeClientKey) || strings.Contains(fmt.Sprint(entry), secondFakeClientKey) {
			t.Fatalf("a log line carries a key value: %+v", entry)
		}
	}

	for _, route := range [][2]string{{http.MethodGet, "/v1/admin/api-keys"}, {http.MethodPost, "/v1/admin/api-keys"}, {http.MethodGet, "/v1/admin/api-keys/x"}, {http.MethodDelete, "/v1/admin/api-keys/x"}} {
		if status, _ := gateway.call(route[0], route[1], "", firstFakeClientKey); status != http.StatusUnauthorized {
			t.Fatalf("%s %s with a client key = %d, want 401", route[0], route[1], status)
		}
	}
}

// Test flow:
//  1. Compose a gateway with one key and the model on the `api_key` tier.
//  2. Revoke the only key and assert 409, with the key still accepted on the next request and its stored row still not revoked.
//  3. Create a second key and assert the first now revokes with 200.
//  4. Revoke the first key again and assert 200 with the same revoked time, and an unknown ID answers 404 on both read and revoke.
func TestRevokingTheLastKeyWhileAModelIsAPIKeyIsRefused(t *testing.T) {
	gateway := composedKeyedGateway(t)
	gateway.createKey(fmt.Sprintf(`{"key":%q}`, firstFakeClientKey))
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", keyedModelSettings)
	firstPath := "/v1/admin/api-keys/" + digestPrefix(firstFakeClientKey)

	gateway.requireAdminStatus(http.StatusConflict, http.MethodDelete, firstPath, "")
	if stored := gateway.requireAdminStatus(http.StatusOK, http.MethodGet, firstPath, ""); stored["revoked"] != false {
		t.Fatalf("the key after a refused revoke = %v, want its row still active", stored)
	}
	if status := gateway.chatStatus(firstFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the key after a refused revoke = %d, want it still accepted", status)
	}

	gateway.createKey(fmt.Sprintf(`{"key":%q}`, secondFakeClientKey))
	revoked := gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, firstPath, "")
	again := gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, firstPath, "")
	if revoked["revoked"] != true || revoked["revoked_at"] == nil || again["revoked_at"] != revoked["revoked_at"] {
		t.Fatalf("revoke = %v, again = %v, want the same revoked time", revoked, again)
	}

	gateway.requireAdminStatus(http.StatusNotFound, http.MethodGet, "/v1/admin/api-keys/00000000", "")
	gateway.requireAdminStatus(http.StatusNotFound, http.MethodDelete, "/v1/admin/api-keys/00000000", "")
}

// Test flow:
//  1. Compose a gateway with no keys and apply a settings write that puts the model on the `api_key` tier.
//  2. Assert the write is refused with 400 and the model stays unlisted.
func TestASettingsWriteThatStrandsAnAPIKeyModelIsRefused(t *testing.T) {
	gateway := composedKeyedGateway(t)

	gateway.requireAdminStatus(http.StatusBadRequest, http.MethodPost, "/v1/admin/settings", keyedModelSettings)

	stored := gateway.requireAdminStatus(http.StatusOK, http.MethodGet, "/v1/admin/settings", "")
	if _, listed := stored["models"]; listed {
		t.Fatalf("settings after a refused write = %v, want no models", stored)
	}
}

// Test flow:
//  1. Save overrides that put the model on the `api_key` tier into a store holding no keys, with the log captured.
//  2. Compose a gateway over that store.
//  3. Assert it starts and logs a warning that an `api_key` model has no active key.
//  4. Turn the kill switch on and off through the settings route and assert both writes are accepted, since neither creates the keyless state.
//  5. Create a key through the admin API and assert the next request with it is accepted.
func TestBootWithNoKeysAndAnAPIKeyModelStartsAndWarns(t *testing.T) {
	logged := logcapture.Install(t)
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)
	records, err := store.Open(os.Getenv("DEVSHARD_STORAGE_DIR"))
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	if err := records.SaveOverrides(context.Background(), apiKeyModelOverrides()); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	gateway := keyedGateway{t: t, handler: composedGateway(t).server.Handler}

	warned := false
	for _, entry := range logged.All() {
		warned = warned || (entry.Level == "warn" && strings.Contains(entry.Msg, config.ErrAPIKeyModelWithoutKey.Error()))
	}
	if !warned {
		t.Fatalf("no warning about the api_key model with no key: %+v", logged.All())
	}
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", `{"enabled":false}`)
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", `{"enabled":true}`)
	gateway.createKey(fmt.Sprintf(`{"key":%q}`, firstFakeClientKey))
	if status := gateway.chatStatus(firstFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the first key on an upgraded gateway = %d, want it accepted", status)
	}
}

// Test flow:
//  1. Save a models block that puts a model on the `api_key` tier into a store whose rotation list no longer names that model.
//  2. Compose a gateway over that store and assert it boots, and a key created through the admin API revokes again with 200.
//  3. Assert a settings write is refused with 400 naming the model, while the block stays stored.
//  4. Assert a settings write that clears the block with a null is accepted.
func TestAStaleStoredModelBlockBootsAndBlocksSettingsWritesUntilCleared(t *testing.T) {
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)
	records, err := store.Open(os.Getenv("DEVSHARD_STORAGE_DIR"))
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	overrides := apiKeyModelOverrides()
	rotation := `[{"model_id":"model-other"}]`
	overrides.RotationModelsJSON = &rotation
	if err := records.SaveOverrides(context.Background(), overrides); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	gateway := keyedGateway{t: t, handler: composedGateway(t).server.Handler}

	created := gateway.createKey(fmt.Sprintf(`{"key":%q}`, firstFakeClientKey))
	gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, "/v1/admin/api-keys/"+fmt.Sprint(created["id"]), "")
	status, response := gateway.admin(http.MethodPost, "/v1/admin/settings", `{"enabled":false}`)
	if status != http.StatusBadRequest || !strings.Contains(response, keyedModel) {
		t.Fatalf("a settings write beside a stale block = %d %s, want 400 naming %s", status, response, keyedModel)
	}
	gateway.requireAdminStatus(http.StatusOK, http.MethodPost, "/v1/admin/settings", `{"models":{"model-a":null},"enabled":false}`)
}

// Test flow:
//  1. Save one active and one revoked key into the store, and put the model on the `api_key` tier.
//  2. Compose a gateway over that store.
//  3. Assert the active key is accepted and the revoked one is refused on the first request.
func TestBootLoadsTheActiveKeysFromTheDatabase(t *testing.T) {
	gatewayEnvironment(t)
	t.Setenv("DEVSHARD_ADMIN_API_KEY", routeTestAdminKey)
	records, err := store.Open(os.Getenv("DEVSHARD_STORAGE_DIR"))
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	ctx := context.Background()
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, key := range []string{firstFakeClientKey, secondFakeClientKey} {
		if err := records.InsertAPIKey(ctx, store.APIKeyRecord{ID: digestPrefix(key), Key: key, CreatedAt: createdAt}); err != nil {
			t.Fatalf("InsertAPIKey(): %v", err)
		}
	}
	if err := records.RevokeAPIKey(ctx, digestPrefix(secondFakeClientKey), createdAt); err != nil {
		t.Fatalf("RevokeAPIKey(): %v", err)
	}
	if err := records.SaveOverrides(ctx, apiKeyModelOverrides()); err != nil {
		t.Fatalf("SaveOverrides(): %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	gateway := keyedGateway{t: t, handler: composedGateway(t).server.Handler}

	if status := gateway.chatStatus(firstFakeClientKey); status == http.StatusUnauthorized {
		t.Fatalf("the active stored key = %d, want it accepted", status)
	}
	if status := gateway.chatStatus(secondFakeClientKey); status != http.StatusUnauthorized {
		t.Fatalf("the revoked stored key = %d, want 401", status)
	}
}

// Test flow:
//  1. Compose a gateway and post a key containing whitespace, one shorter than 16 characters, one equal to the admin key, and a body naming an unknown field.
//  2. Assert each is refused with 400.
//  3. Assert the list is still empty.
func TestAKeyThatCannotWorkIsRefusedAtCreate(t *testing.T) {
	gateway := composedKeyedGateway(t)

	for _, body := range []string{
		`{"key":"a client key with spaces"}`,
		`{"key":"too-short"}`,
		fmt.Sprintf(`{"key":%q}`, routeTestAdminKey),
		`{"keys":"a-misspelled-field-name"}`,
	} {
		gateway.requireAdminStatus(http.StatusBadRequest, http.MethodPost, "/v1/admin/api-keys", body)
	}

	if listing := gateway.requireAdminStatus(http.StatusOK, http.MethodGet, "/v1/admin/api-keys", ""); len(listing["api_keys"].([]any)) != 0 {
		t.Fatalf("list after refused creates = %v, want none", listing)
	}
}

// Test flow:
//  1. Compose a gateway, create a key with a supplied value, and post the same value again.
//  2. Assert the second create is refused with 409.
//  3. Create a second key, revoke the first, and post the first value again.
//  4. Assert the create is still refused with 409, so a revoked key's ID is never reused, and the list holds two rows.
func TestACreateWhoseIDIsTakenIsRefusedRevokedOrNot(t *testing.T) {
	gateway := composedKeyedGateway(t)
	body := fmt.Sprintf(`{"key":%q}`, firstFakeClientKey)
	gateway.createKey(body)

	gateway.requireAdminStatus(http.StatusConflict, http.MethodPost, "/v1/admin/api-keys", body)

	gateway.createKey(fmt.Sprintf(`{"key":%q}`, secondFakeClientKey))
	gateway.requireAdminStatus(http.StatusOK, http.MethodDelete, "/v1/admin/api-keys/"+digestPrefix(firstFakeClientKey), "")
	gateway.requireAdminStatus(http.StatusConflict, http.MethodPost, "/v1/admin/api-keys", body)

	if listing := gateway.requireAdminStatus(http.StatusOK, http.MethodGet, "/v1/admin/api-keys", ""); len(listing["api_keys"].([]any)) != 2 {
		t.Fatalf("list = %v, want two rows", listing)
	}
}

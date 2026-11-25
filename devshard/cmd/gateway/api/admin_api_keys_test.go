package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Post a create with no key and a padded label, then one with a supplied key.
//  2. Assert the operator got a generated `sk-` key whose ID is the key's SHA-256 prefix and a trimmed label, then the supplied key as given.
//  3. Assert each response carries the key and the same ID the request records will name the key by.
func TestACreateGeneratesAKeyUnlessOneIsSupplied(t *testing.T) {
	live := newHarness(t)

	generated := live.request(t, http.MethodPost, "/v1/admin/api-keys", `{"label":"  nightly  "}`, adminHeaders())
	supplied := live.request(t, http.MethodPost, "/v1/admin/api-keys", `{"key":"fake-supplied-key-0001"}`, adminHeaders())

	if generated.Code != http.StatusOK || supplied.Code != http.StatusOK {
		t.Fatalf("statuses = %d %d, want 200 200: %s %s", generated.Code, supplied.Code, generated.Body, supplied.Body)
	}
	created := live.operations.createdKeys
	if len(created) != 2 || !strings.HasPrefix(created[0].Key, "sk-") || created[0].Label != "nightly" || created[1].Key != "fake-supplied-key-0001" {
		t.Fatalf("operator received %+v, want a generated key labelled nightly, then the supplied key", created)
	}
	for index, recorder := range []string{generated.Body.String(), supplied.Body.String()} {
		var answer struct{ ID, Key string }
		if err := json.Unmarshal([]byte(recorder), &answer); err != nil {
			t.Fatalf("response %d: %v", index, err)
		}
		if answer.Key != created[index].Key || answer.ID != apiKeyIDFor(answer.Key) || answer.ID != created[index].ID {
			t.Fatalf("response %d = %s, want the stored key and its digest prefix %s", index, recorder, apiKeyIDFor(answer.Key))
		}
	}
}

// Test flow:
//  1. For each refusal the operator answers a create or a revoke with, set it as the operator's error.
//  2. Send the create, or a revoke of a stored key, through the admin route.
//  3. Assert an invalid key answers 400, a taken ID 409, and a last key 409.
func TestAKeyWriteRefusalAnswersItsStatus(t *testing.T) {
	testCases := []struct {
		name    string
		refusal error
		method  string
		path    string
		want    int
	}{
		{"invalid key", fmt.Errorf("%w: api_keys[0]: must be at least 16 characters", config.ErrInvalid), http.MethodPost, "/v1/admin/api-keys", http.StatusBadRequest},
		{"id taken", fmt.Errorf("%w: 1f2e3d4c", store.ErrAPIKeyIDTaken), http.MethodPost, "/v1/admin/api-keys", http.StatusConflict},
		{"last key", fmt.Errorf("%w: access marks a model api_key", ErrLastAPIKey), http.MethodDelete, "/v1/admin/api-keys/aaaaaaaa", http.StatusConflict},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			live.control.apiKeys = []store.APIKeyRecord{{ID: "aaaaaaaa", Key: "fake-stored-key-000001"}}
			live.operations.err = testCase.refusal

			recorder := live.request(t, testCase.method, testCase.path, "{}", adminHeaders())

			if recorder.Code != testCase.want {
				t.Fatalf("status = %d %s, want %d", recorder.Code, recorder.Body, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Store one active key and one revoked key.
//  2. Revoke an unknown ID, and the already revoked key.
//  3. Assert the unknown ID answers 404, the revoked key answers 200 with its revoked time, and the operator was never asked to write.
func TestRevokingAnUnknownOrAlreadyRevokedKeyWritesNothing(t *testing.T) {
	live := newHarness(t)
	revokedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	live.control.apiKeys = []store.APIKeyRecord{
		{ID: "aaaaaaaa", Key: "fake-stored-key-000001"},
		{ID: "bbbbbbbb", Key: "fake-stored-key-000002", RevokedAt: revokedAt},
	}

	unknown := live.request(t, http.MethodDelete, "/v1/admin/api-keys/cccccccc", "", adminHeaders())
	revoked := live.request(t, http.MethodDelete, "/v1/admin/api-keys/bbbbbbbb", "", adminHeaders())

	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown ID = %d, want 404", unknown.Code)
	}
	if revoked.Code != http.StatusOK || !strings.Contains(revoked.Body.String(), store.FormatTime(revokedAt)) {
		t.Fatalf("already revoked = %d %s, want 200 with its revoked time", revoked.Code, revoked.Body)
	}
	if calls := live.operations.recordedCalls(); len(calls) != 0 {
		t.Fatalf("operator calls = %v, want none", calls)
	}
}

// Test flow:
//  1. Start a harness whose only client key is created the way the admin route does, by its `idOfAPIKey`.
//  2. Assert `idOfAPIKey` equals the first eight hex characters of the key's SHA-256, the id `TestEachRequestRecordNamesItsAPIKey` asserts on request lines.
//  3. Assert a request with the key resolves to credentials whose id is that same value.
func TestTheIDOfADatabaseKeyIsTheDigestPrefixRequestsAreLoggedUnder(t *testing.T) {
	const key = "fake-client-key-0000000001"
	live := newHarness(t, func(configuration *config.Config) { configuration.Server.APIKeys = []string{key} })

	if got := idOfAPIKey(key); got != apiKeyIDFor(key) {
		t.Fatalf("idOfAPIKey() = %s, want %s", got, apiKeyIDFor(key))
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	if got := live.server.resolveCredentials(request).caller(); got != idOfAPIKey(key) {
		t.Fatalf("request caller = %s, want %s", got, idOfAPIKey(key))
	}
}

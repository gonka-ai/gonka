package api

import (
	"net/http"
	"strings"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Seed the harness's admin control with a devshard record and a rotation status.
//  2. Request the /v1/admin/state endpoint.
//  3. Assert the response body carries the expected fields in snake_case.
//  4. Assert the body does not carry the underlying Go struct field names.
func TestAdminStateSpellsStorageRowsInSnakeCase(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = []store.DevshardRecord{{EscrowID: "47452", PrivateKeyEnv: "GATEWAY_PRIVATE_KEY", ChainEpoch: 7, Amount: 1000, GoneFromChain: true}}
	live.control.rotation = []store.RotationStatus{{Model: "model-a", Stage: "prepared"}}

	body := live.request(t, http.MethodGet, "/v1/admin/state", "", adminHeaders()).Body.String()

	for _, want := range []string{
		`"id":"47452"`,
		`"private_key_env":"GATEWAY_PRIVATE_KEY"`,
		`"rotation_epoch":0`,
		`"chain_epoch":7`,
		`"amount":1000`,
		`"gone_from_chain":true`,
		`"stage":"prepared"`,
		`"model_id":"model-a"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("state body %s is missing %s", body, want)
		}
	}
	for _, goFieldName := range []string{"EscrowID", "PrivateKeyEnv", "RotationEpoch", "Stage", "ChainEpoch", "GoneFromChain"} {
		if strings.Contains(body, goFieldName) {
			t.Fatalf("state body %s still carries the Go field name %s", body, goFieldName)
		}
	}
}

// Test flow:
//  1. For each case, varying ForceUpstreamStreaming across on and rolled back, build filter options from that config.
//  2. Assert KeepClientStream is the opposite of ForceUpstreamStreaming in each case.
func TestTheForcedStreamingSwitchReachesTheFilters(t *testing.T) {
	tests := []struct {
		name                 string
		forceUpstream        bool
		wantKeepClientStream bool
	}{
		{name: "forcing on, as it ships", forceUpstream: true, wantKeepClientStream: false},
		{name: "forcing rolled back", forceUpstream: false, wantKeepClientStream: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			options := filterOptions(config.Limits{ForceUpstreamStreaming: testCase.forceUpstream}, false, nil, nil)

			if options.KeepClientStream != testCase.wantKeepClientStream {
				t.Errorf("KeepClientStream = %v, want %v", options.KeepClientStream, testCase.wantKeepClientStream)
			}
		})
	}
}

// Test flow:
//  1. Wire a vocabulary that answers 1000 and records every model it is asked about.
//  2. Send a chat request with stop_token_ids for the served model, then one for a model no escrow serves.
//  3. Assert the served request passes with one lookup for its model, and the unserved one is refused without a lookup: the lookup can reach the chain and caches per model.
func TestTheStopTokenVocabularyIsAskedOnlyForAServedModel(t *testing.T) {
	live := newHarness(t)
	var askedModels []string
	live.server.stopTokenVocabulary = func(model string) int {
		askedModels = append(askedModels, model)
		return 1000
	}

	served := live.request(t, http.MethodPost, "/v1/chat/completions", `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"stop_token_ids":[7]}`, nil)
	unserved := live.request(t, http.MethodPost, "/v1/chat/completions", `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}],"stop_token_ids":[7]}`, nil)

	if served.Code != http.StatusOK {
		t.Errorf("served model status = %d, want %d: %s", served.Code, http.StatusOK, served.Body.String())
	}
	if unserved.Code == http.StatusOK {
		t.Errorf("unserved model status = %d, want a refusal", unserved.Code)
	}
	if len(askedModels) != 1 || askedModels[0] != "qwen" {
		t.Errorf("vocabulary asked for %v, want only [qwen]", askedModels)
	}
}

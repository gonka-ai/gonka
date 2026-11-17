package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Table-driven: each case calls one route whose answer keys are fixed, with a store holding two escrows and one rotation row, and a live session where the route reads one.
//  2. Decode the body as JSON and walk the case's key path.
//  3. Assert every fixed key is present and the gateway's former spelling is absent.
func TestRoutesAnswerUnderTheFixedKeyNames(t *testing.T) {
	testCases := []struct {
		name        string
		method      string
		target      string
		body        string
		liveEscrow  bool
		presentKeys [][]string
		absentKeys  [][]string
	}{
		{
			name: "status names each escrow by id and counts its active requests", method: http.MethodGet, target: "/v1/status",
			presentKeys: [][]string{{"devshards", "0", "id"}, {"devshards", "0", "active_requests"}, {"capacity", "0", "max_concurrent_requests_per_10000_weight"}},
			absentKeys:  [][]string{{"devshards", "0", "escrow_id"}, {"devshards", "0", "active_users"}, {"capacity", "0", "max_concurrent_per_10000_weight"}},
		},
		{
			name: "the admin listing names each escrow by id", method: http.MethodGet, target: "/v1/admin/devshards",
			presentKeys: [][]string{{"devshards", "0", "id"}},
			absentKeys:  [][]string{{"devshards", "0", "escrow_id"}},
		},
		{
			name: "the rotation debug view lists the latest rows by model_id", method: http.MethodGet, target: "/v1/debug/rotation",
			presentKeys: [][]string{{"latest", "0", "model_id"}},
			absentKeys:  [][]string{{"rotation"}, {"latest", "0", "model"}},
		},
		{
			name: "the never-trust list is suspicious_hosts", method: http.MethodGet, target: "/v1/admin/suspicious-hosts",
			presentKeys: [][]string{{"suspicious_hosts"}},
			absentKeys:  [][]string{{"hosts"}},
		},
		{
			name: "memstats counts loaded_runtimes", method: http.MethodGet, target: "/v1/debug/memstats",
			presentKeys: [][]string{{"loaded_runtimes"}},
			absentKeys:  [][]string{{"loaded_escrows"}},
		},
		{
			name: "an accounting reset reports escrows_removed", method: http.MethodPost, target: "/v1/admin/accounting/reset/370",
			presentKeys: [][]string{{"escrows_removed"}},
			absentKeys:  [][]string{{"escrows"}},
		},
		{
			name: "adding an escrow answers with its id", method: http.MethodPost, target: "/v1/admin/devshards",
			body:        `{"id":"11","model":"qwen","private_key_env":"GATEWAY_KEY_11"}`,
			presentKeys: [][]string{{"id"}},
			absentKeys:  [][]string{{"escrow_id"}},
		},
		{
			name: "deleting an escrow answers with its id", method: http.MethodDelete, target: "/v1/admin/devshards/9",
			presentKeys: [][]string{{"id"}, {"deleted"}},
			absentKeys:  [][]string{{"escrow_id"}},
		},
		{
			name: "the signature debug view lists nonces", method: http.MethodGet, target: "/devshard/" + liveEscrowID + "/v1/debug/signatures", liveEscrow: true,
			presentKeys: [][]string{{"nonces"}},
			absentKeys:  [][]string{{"signatures"}},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)
			if testCase.liveEscrow {
				live = harnessWithLiveEscrow(t)
			}
			live.control.devshards = []store.DevshardRecord{{EscrowID: "7", Model: "qwen", Active: true}, {EscrowID: "9", Model: "qwen"}}
			live.control.rotation = []store.RotationStatus{{Model: "qwen", Stage: "prepared"}}

			recorder := live.request(t, testCase.method, testCase.target, testCase.body, adminHeaders())

			if recorder.Code != http.StatusOK {
				t.Fatalf("%s %s = %d %s, want 200", testCase.method, testCase.target, recorder.Code, recorder.Body.String())
			}
			var decoded any
			if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
			}
			for _, path := range testCase.presentKeys {
				if _, found := walkJSONPath(decoded, path); !found {
					t.Errorf("body %s has no %v", recorder.Body.String(), path)
				}
			}
			for _, path := range testCase.absentKeys {
				if _, found := walkJSONPath(decoded, path); found {
					t.Errorf("body %s still carries %v", recorder.Body.String(), path)
				}
			}
		})
	}
}

func walkJSONPath(node any, path []string) (any, bool) {
	for _, step := range path {
		switch typed := node.(type) {
		case map[string]any:
			next, found := typed[step]
			if !found {
				return nil, false
			}
			node = next
		case []any:
			index, err := strconv.Atoi(step)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			node = typed[index]
		default:
			return nil, false
		}
	}
	return node, true
}

// Test flow:
//  1. Post bodies using the fixed key names to the escrow-creation, add and import routes.
//  2. Assert each route answers 200.
//  3. Assert the operation received every field under its fixed key: model_id on creation, id on add and import, active on import.
func TestRequestsAreReadUnderTheFixedKeyNames(t *testing.T) {
	live := newHarness(t)

	for target, body := range map[string]string{
		"/v1/admin/escrows":          `{"model_id":"qwen","amount":10,"private_key_env":"KEY"}`,
		"/v1/admin/devshards":        `{"id":"11","model":"qwen","private_key_env":"KEY"}`,
		"/v1/admin/devshards/import": `{"id":"12","source_path":"/tmp/x","private_key_env":"KEY","active":true}`,
	} {
		if recorder := live.request(t, http.MethodPost, target, body, adminHeaders()); recorder.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s, want 200", target, recorder.Code, recorder.Body.String())
		}
	}

	if len(live.operations.created) != 1 || live.operations.created[0].Model != "qwen" {
		t.Errorf("created = %+v, want the model read from model_id", live.operations.created)
	}
	if len(live.operations.added) != 1 || live.operations.added[0].EscrowID != "11" {
		t.Errorf("added = %+v, want the escrow read from id", live.operations.added)
	}
	if len(live.operations.imported) != 1 || live.operations.imported[0].EscrowID != "12" || !live.operations.imported[0].Activate {
		t.Errorf("imported = %+v, want the escrow read from id and activated from active", live.operations.imported)
	}
}

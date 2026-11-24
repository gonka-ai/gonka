package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"devshard/cmd/gateway/store"
)

type batchDeactivateResult struct {
	EscrowID string `json:"escrow_id"`
	Status   int    `json:"status"`
	Error    string `json:"error"`
}

type batchDeactivateAnswer struct {
	Deactivated int
	Failed      int
	Results     map[string]batchDeactivateResult
}

// decodeBatchDeactivate reads one result per line and the closing {deactivated, failed} line.
func decodeBatchDeactivate(t *testing.T, body []byte) batchDeactivateAnswer {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	var summary struct {
		Deactivated int `json:"deactivated"`
		Failed      int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatalf("decoding the summary line of %s: %v", body, err)
	}
	answer := batchDeactivateAnswer{
		Deactivated: summary.Deactivated, Failed: summary.Failed, Results: map[string]batchDeactivateResult{},
	}
	for _, line := range lines[:len(lines)-1] {
		var result batchDeactivateResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatalf("decoding result line %s: %v", line, err)
		}
		answer.Results[result.EscrowID] = result
	}
	return answer
}

// Test flow:
//  1. Register escrows "7" and "9".
//  2. POST a batch deactivate for "7", "9", a repeated "7", and an unregistered "404".
//  3. Assert the answer is 200 NDJSON with deactivated=2 and failed=1, and "404" answers 404.
//  4. Assert the deactivate operation ran once for each registered escrow, and for no other.
func TestABatchDeactivateAnswersForEveryEscrowItWasGiven(t *testing.T) {
	live := newHarness(t)
	live.control.devshards = []store.DevshardRecord{
		{EscrowID: "7", Model: "qwen"},
		{EscrowID: "9", Model: "qwen"},
	}

	response := live.request(t, http.MethodPost, "/v1/admin/devshards/deactivate",
		`{"escrow_ids":["7","9","7","404"]}`, adminHeaders())

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", response.Code, response.Body.String())
	}
	answer := decodeBatchDeactivate(t, response.Body.Bytes())
	if answer.Deactivated != 2 || answer.Failed != 1 {
		t.Fatalf("counts = deactivated %d, failed %d, want 2 and 1", answer.Deactivated, answer.Failed)
	}
	if got := answer.Results["404"].Status; got != http.StatusNotFound {
		t.Fatalf("status of the unregistered escrow = %d, want 404", got)
	}
	if deactivated := live.operations.deactivatedEscrows(); !slices.Equal(deactivated, []string{"7", "9"}) {
		t.Fatalf("deactivated escrows = %v, want [7 9]", deactivated)
	}
}

// Test flow:
//  1. Build an escrow-ID list one entry past `batchEscrowLimit`.
//  2. POST each refused body (no body, an empty list, the oversized list, a batch size past the ceiling) to the batch deactivate route.
//  3. Assert each answers 400 and no deactivation ran.
func TestABatchDeactivateRefusesAListItCannotAnswerFor(t *testing.T) {
	oversized := make([]string, batchEscrowLimit+1)
	for index := range oversized {
		oversized[index] = strconv.Itoa(index)
	}
	oversizedBody, err := json.Marshal(DeactivateDevshardsRequest{EscrowIDs: oversized})
	if err != nil {
		t.Fatalf("building the oversized body: %v", err)
	}
	testCases := []struct {
		name string
		body string
	}{
		{name: "no body at all", body: ""},
		{name: "an empty list", body: `{"escrow_ids":[]}`},
		{name: "more escrows than one call takes", body: string(oversizedBody)},
		{name: "a batch size past the ceiling", body: `{"escrow_ids":["7"],"batch_size":` + strconv.Itoa(maxBatchSize+1) + `}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			live := newHarness(t)

			response := live.request(t, http.MethodPost, "/v1/admin/devshards/deactivate", testCase.body, adminHeaders())

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (%s), want 400", response.Code, response.Body.String())
			}
			if calls := live.operations.recordedCalls(); len(calls) != 0 {
				t.Fatalf("a refused list reached the deactivate path: %v", calls)
			}
		})
	}
}

package e2e

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Start the three-host environment with every executor storing and signing its real answer but streaming a changed one.
//  2. Send one streaming completion.
//  3. Assert the client received the changed answer, so the swap reached the wire.
//  4. Wait until that stream's Finish is queued. A pairwise loser can still be in flight when the HTTP body returns, and the Finish is queued only after that background race settles.
//  5. Send one more completion. Its diff applies the queued Finish, which this stream's own diffs were composed too early to carry.
//  6. Assert the gateway judged the swapped stream a mismatch against the signed Finish and bound none.
func TestE2E_ServedBindingCatchesAStreamTheExecutorDidNotSign(t *testing.T) {
	env, client := startNonStreamingEnvWithOptions(t, e2eEnvOptions{hostEnv: map[string]string{
		e2econfig.StubInferenceTamperedStreamEnv: "true",
		"DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED": "true",
	}})

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "served binding against a swapped stream")
	testutil.DebugLogf(t, "client stream: %v", stream.Events)
	require.True(t, testutil.StreamCarries(stream, "tampered"), "the swapped answer never reached the client: %v", stream.Events)

	waitForPendingFinish(t, client, env.clientURL, inferenceIDFromStream(t, stream))
	testutil.SendStreamingCompletion(t, client, env.clientURL, "apply the swapped stream's finish")

	bindings := testutil.WaitServedBindings(t, client, env.clientURL, servedBindingWindow, func(bindings testutil.ServedBindings) bool {
		return bindings["mismatch"] >= 1
	})
	require.GreaterOrEqual(t, bindings["mismatch"], float64(1), "a stream the executor did not sign must be a mismatch: %v", bindings)
	require.Zero(t, bindings["bound"], "no swapped stream may bind: %v", bindings)
}

func inferenceIDFromStream(t *testing.T, stream testutil.StreamResponse) uint64 {
	t.Helper()
	for _, event := range stream.Events {
		var chunk struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(event), &chunk); err != nil || chunk.ID == "" {
			continue
		}
		suffix := chunk.ID[strings.LastIndex(chunk.ID, "-")+1:]
		id, err := strconv.ParseUint(suffix, 10, 64)
		require.NoError(t, err, "inference id in %q", chunk.ID)
		return id
	}
	t.Fatal("stream carried no inference id")
	return 0
}

func waitForPendingFinish(t *testing.T, client *http.Client, clientURL string, inferenceID uint64) {
	t.Helper()
	deadline := time.Now().Add(servedBindingWindow)
	var pending map[string]any
	for {
		pending = testutil.GetJSON(t, client, clientURL+"/v1/debug/pending")
		if pendingHasFinish(pending, inferenceID) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("finish %d was not queued before the follow-up diff: %v", inferenceID, pending["pending"])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func pendingHasFinish(state map[string]any, inferenceID uint64) bool {
	txs, _ := state["pending"].([]any)
	for _, item := range txs {
		tx, _ := item.(map[string]any)
		if tx["type"] != "finish" {
			continue
		}
		id, _ := tx["id"].(float64)
		if uint64(id) == inferenceID {
			return true
		}
	}
	return false
}

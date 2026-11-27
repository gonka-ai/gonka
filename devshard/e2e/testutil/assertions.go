package testutil

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func RequireOpenAIStream(t *testing.T, stream StreamResponse) {
	t.Helper()
	require.Contains(t, stream.ContentType, "text/event-stream")
	require.NotEmpty(t, stream.Events, "SSE stream should include data events")

	doneCount := 0
	contentSeen := false
	for i, event := range stream.Events {
		if event == "[DONE]" {
			doneCount++
			require.Equal(t, len(stream.Events)-1, i, "[DONE] should only appear as the final SSE data event")
			continue
		}
		requireNoProtocolLeak(t, event)
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(event), &payload), "SSE data event should be JSON: %s", event)
		require.Contains(t, payload, "choices", "SSE JSON event should keep OpenAI-compatible choices")
		require.NotEqual(t, "chat.completion", payload["object"], "raw non-streaming chat.completion object should not leak into SSE")
		if streamEventHasContent(t, payload) {
			contentSeen = true
		}
	}

	require.True(t, contentSeen, "SSE stream should include at least one content-bearing event")
	require.Equal(t, 1, doneCount, "SSE stream should include exactly one [DONE] event")
	require.Equal(t, "[DONE]", stream.Events[len(stream.Events)-1], "final SSE data event should be [DONE]")
}

func RequireOpenAICompletion(t *testing.T, resp map[string]any) {
	t.Helper()
	require.Contains(t, resp, "choices", "completion response should include choices")
	choices, ok := resp["choices"].([]any)
	require.True(t, ok, "completion choices should be an array")
	require.NotEmpty(t, choices, "completion response should include at least one choice")
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		require.True(t, ok, "completion choice should be an object")
		if message, ok := choice["message"].(map[string]any); ok {
			require.NotEmpty(t, message["content"], "completion message should include content")
			continue
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			require.NotEmpty(t, delta["content"], "completion delta should include content")
			continue
		}
		t.Fatalf("completion choice should include message or delta: %v", choice)
	}
}

func RequireOpenAINonStreamingCompletion(t *testing.T, resp RawResponse) {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode, "completion should return success: %s", resp.Body)
	require.NotContains(t, resp.ContentType, "text/event-stream", "non-streaming completion should not return SSE")
	require.NotContains(t, resp.Body, "data:", "non-streaming completion should not return SSE data lines")
	require.NotNil(t, resp.JSON, "non-streaming completion should return JSON: %s", resp.Body)
	requireNoProtocolLeak(t, resp.Body)
	require.Contains(t, resp.JSON, "choices", "completion response should include choices")
	choices, ok := resp.JSON["choices"].([]any)
	require.True(t, ok, "completion choices should be an array")
	require.NotEmpty(t, choices, "completion response should include at least one choice")
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		require.True(t, ok, "completion choice should be an object")
		require.NotContains(t, choice, "delta", "non-streaming choices should not use streaming delta shape")
		require.NotContains(t, choice, "logprobs", "non-streaming choices should not expose logprobs")
		message, ok := choice["message"].(map[string]any)
		require.True(t, ok, "non-streaming completion choice should include message")
		require.NotEmpty(t, message["content"], "completion message should include content")
	}
}

func requireNoProtocolLeak(t *testing.T, event string) {
	t.Helper()
	lower := strings.ToLower(event)
	for _, marker := range []string{"devshard", "receipt", "metadata", "state_root", "signature", "nonce", "escrow"} {
		require.NotContains(t, lower, marker, "SSE event should not expose devshard protocol metadata")
	}
}

func streamEventHasContent(t *testing.T, payload map[string]any) bool {
	t.Helper()
	choices, ok := payload["choices"].([]any)
	require.True(t, ok, "choices should be an array")
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		require.True(t, ok, "choice should be an object")
		require.NotContains(t, choice, "logprobs", "streaming choices should not expose logprobs")
		_, hasDelta := choice["delta"].(map[string]any)
		_, hasMessage := choice["message"].(map[string]any)
		require.True(t, hasDelta || hasMessage, "streaming choice should include delta or message")
		if delta, ok := choice["delta"].(map[string]any); ok && delta["content"] != "" {
			return true
		}
		if message, ok := choice["message"].(map[string]any); ok && message["content"] != "" {
			return true
		}
	}
	return false
}

func RequireSignatureCollectionQuorum(t *testing.T, collection map[string]any, nonce uint64, totalSlots int) {
	t.Helper()
	require.Equal(t, nonce, NumericField(t, collection, "nonce"))
	require.Equal(t, uint64(totalSlots), NumericField(t, collection, "total_slots"))
	threshold := NumericField(t, collection, "quorum_threshold")
	weight := NumericField(t, collection, "sig_weight")
	require.NotZero(t, threshold, "signature quorum threshold should be non-zero")
	require.LessOrEqual(t, threshold, uint64(totalSlots), "signature quorum threshold should not exceed total slots")
	require.GreaterOrEqual(t, weight, threshold, "latest nonce should reach signature quorum")
	hasQuorum, ok := collection["has_quorum"].(bool)
	require.True(t, ok, "signature collection has_quorum should be a boolean")
	require.True(t, hasQuorum, "latest nonce should report signature quorum")
}

// RequireSignatureStatusQuorum asserts the escrow's signature status holds quorum through nonce, with the nonce's own entry at quorum.
func RequireSignatureStatusQuorum(t *testing.T, status map[string]any, nonce uint64, totalSlots int) {
	t.Helper()
	require.GreaterOrEqual(t, NumericField(t, status, "highest_quorum_nonce"), nonce,
		"latest nonce should be at or below the highest nonce with quorum")
	hasQuorum, ok := status["has_quorum"].(bool)
	require.True(t, ok, "signature status has_quorum should be a boolean")
	require.True(t, hasQuorum, "signature status should report quorum")

	nonces, ok := status["nonces"].([]any)
	require.True(t, ok, "signature status nonces should be an array")
	for _, raw := range nonces {
		entry, ok := raw.(map[string]any)
		require.True(t, ok, "signature status entries should be objects")
		if NumericField(t, entry, "nonce") != nonce {
			continue
		}
		require.Equal(t, uint64(totalSlots), NumericField(t, entry, "total_slots"))
		require.GreaterOrEqual(t, NumericField(t, entry, "sig_weight"), signatureQuorumThreshold(totalSlots),
			"latest nonce status entry should carry the slot weight of a 2/3+1 quorum")
		hasQuorum, ok := entry["has_quorum"].(bool)
		require.True(t, ok, "latest nonce has_quorum should be a boolean")
		require.True(t, hasQuorum, "latest nonce status entry should report quorum")
		return
	}
	t.Fatalf("signature status should include latest nonce %d: %v", nonce, status)
}

// signatureQuorumThreshold is the protocol's 2/3+1 slot-weighted quorum, computed apart from the gateway's own has_quorum.
func signatureQuorumThreshold(totalSlots int) uint64 {
	return uint64(2*totalSlots/3 + 1)
}

func RequireGossipNonceConvergence(t *testing.T, statuses []GossipNonceStatus, nonce uint64) {
	t.Helper()
	require.NotEmpty(t, statuses, "gossip status should be collected from every host")
	want := statuses[0]
	require.True(t, want.Seen, "host 0 should observe the gossiped nonce")
	require.Equal(t, nonce, want.Nonce)
	require.NotEmpty(t, want.StateHash, "gossip status should include a state hash")
	require.NotEmpty(t, want.StateSig, "gossip status should include a state signature")

	for index, status := range statuses[1:] {
		require.Truef(t, status.Seen, "host %d should observe the gossiped nonce", index+1)
		require.Equalf(t, nonce, status.Nonce, "host %d should report the expected nonce", index+1)
		require.Equalf(t, want.StateHash, status.StateHash, "host %d should observe the same state hash", index+1)
		require.Equalf(t, want.StateSig, status.StateSig, "host %d should observe the same state signature", index+1)
		require.Equalf(t, want.SenderSlot, status.SenderSlot, "host %d should observe the same signature sender", index+1)
	}
}

func RequireTimeoutInferenceTransaction(t *testing.T, timeout TimeoutInferenceTransaction, inferenceID uint64, reason types.TimeoutReason, voteThreshold uint64, executorSlot uint32) {
	t.Helper()
	require.Equal(t, inferenceID, timeout.InferenceID)
	require.Equal(t, reason, timeout.Reason)
	require.Greater(t, uint64(len(timeout.VoterSlots)), voteThreshold, "timeout votes should exceed the threshold")
	require.NotContains(t, timeout.VoterSlots, executorSlot, "executor must not vote for its own timeout")

	seen := make(map[uint32]struct{}, len(timeout.VoterSlots))
	for _, slot := range timeout.VoterSlots {
		if _, exists := seen[slot]; exists {
			t.Fatalf("duplicate timeout vote from slot %d", slot)
		}
		seen[slot] = struct{}{}
	}
}

func optionalNumericField(t *testing.T, obj map[string]any, field string) uint64 {
	t.Helper()
	value, ok := obj[field]
	if !ok {
		return 0
	}
	return NumericValue(t, value, field)
}

func NumericField(t *testing.T, obj map[string]any, field string) uint64 {
	t.Helper()
	value, ok := obj[field]
	require.Truef(t, ok, "missing numeric field %q", field)
	return NumericValue(t, value, field)
}

func NumericValue(t *testing.T, value any, field string) uint64 {
	t.Helper()
	switch v := value.(type) {
	case float64:
		require.GreaterOrEqual(t, v, float64(0), "field %q must be non-negative", field)
		return uint64(v)
	case json.Number:
		n, err := v.Int64()
		require.NoError(t, err)
		require.GreaterOrEqual(t, n, int64(0), "field %q must be non-negative", field)
		return uint64(n)
	default:
		t.Fatalf("field %q has non-numeric type %T", field, value)
		return 0
	}
}

// EngineBookkeepingFields are the serving engine's own, which no client may ever be shown.
var EngineBookkeepingFields = []string{`"token_ids"`, `"prompt_token_ids"`, `"prompt_logprobs"`}

// RequireNoEngineBookkeeping fails when a stream carries fields only the serving engine reads.
func RequireNoEngineBookkeeping(t *testing.T, stream StreamResponse) {
	t.Helper()
	for _, event := range stream.Events {
		for _, field := range EngineBookkeepingFields {
			require.NotContains(t, event, field, "engine bookkeeping reached the client: %s", event)
		}
	}
}

// StreamCarries reports whether any data event holds the quoted field.
func StreamCarries(stream StreamResponse, quotedField string) bool {
	for _, event := range stream.Events {
		if strings.Contains(event, quotedField) {
			return true
		}
	}
	return false
}

package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

func RequireSettlementContract(t *testing.T, settlement map[string]any) {
	t.Helper()
	require.NotEmpty(t, settlement["escrow_id"])
	require.NotEmpty(t, settlement["version"])
	require.NotEmpty(t, settlement["state_root"])
	require.NotZero(t, NumericField(t, settlement, "nonce"))

	signatures, ok := settlement["signatures"].([]any)
	require.True(t, ok, "signatures should be a JSON array")
	require.NotEmpty(t, signatures)
	seenSlots := make(map[uint64]struct{}, len(signatures))
	for _, raw := range signatures {
		sig, ok := raw.(map[string]any)
		require.True(t, ok, "signature entries should be objects")
		slotID := NumericField(t, sig, "slot_id")
		require.NotEmpty(t, sig["signature"])
		if _, exists := seenSlots[slotID]; exists {
			t.Fatalf("duplicate settlement signature for slot %d", slotID)
		}
		seenSlots[slotID] = struct{}{}
	}
}

func RequireCompletedValidationContract(t *testing.T, settlement map[string]any) {
	t.Helper()
	progress := validationProgressFromHostStats(t, settlement["host_stats"])
	require.Greater(t, progress.required, uint64(0), "settlement should require at least one validation")
	require.Equal(t, progress.required, progress.completed,
		"settlement should include every required validation as completed")
}

func RequireSettlementHostStats(t *testing.T, settlement map[string]any, hostCount int) {
	t.Helper()
	stats, ok := settlement["host_stats"].([]any)
	require.True(t, ok, "host_stats should be a JSON array")
	require.Len(t, stats, hostCount, "settlement should include one host_stats entry per host")
	for _, raw := range stats {
		stat, ok := raw.(map[string]any)
		require.True(t, ok, "host_stats entries should be objects")
		_ = NumericField(t, stat, "slot_id")
		_ = optionalNumericField(t, stat, "required_validations")
		_ = optionalNumericField(t, stat, "completed_validations")
	}
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

func RequireSignatureStatusQuorum(t *testing.T, status map[string]any, nonce uint64, totalSlots int) {
	t.Helper()
	require.Equal(t, nonce, NumericField(t, status, "current_nonce"))
	require.Equal(t, uint64(totalSlots), NumericField(t, status, "total_slots"))
	require.GreaterOrEqual(t, NumericField(t, status, "highest_quorum_nonce"), nonce,
		"latest nonce should be the highest nonce with quorum")
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
		require.GreaterOrEqual(t, NumericField(t, entry, "sig_weight"), NumericField(t, status, "quorum_threshold"),
			"latest nonce status entry should reach quorum")
		hasQuorum, ok := entry["has_quorum"].(bool)
		require.True(t, ok, "latest nonce has_quorum should be a boolean")
		require.True(t, hasQuorum, "latest nonce status entry should report quorum")
		return
	}
	t.Fatalf("signature status should include latest nonce %d", nonce)
}

func HasInferenceValidationTarget(t *testing.T, state map[string]any, target uint64) (bool, string) {
	t.Helper()
	inferences, ok := state["inferences"].(map[string]any)
	require.True(t, ok, "state inferences should be an object")

	counts := map[uint64]uint64{}
	for inferenceID, raw := range inferences {
		inference, ok := raw.(map[string]any)
		require.True(t, ok, "state inference %s should be an object", inferenceID)
		validatedBy, ok := inference["validated_by"].([]any)
		if !ok {
			continue
		}
		for _, rawSlot := range validatedBy {
			slotID := NumericValue(t, rawSlot, "validated_by slot")
			counts[slotID]++
		}
	}

	summary := ""
	reached := false
	for slotID, completed := range counts {
		if summary != "" {
			summary += "; "
		}
		summary += fmt.Sprintf("slot %d completed=%d", slotID, completed)
		if completed >= target {
			reached = true
		}
	}
	if summary == "" {
		summary = fmt.Sprintf("%d inferences, none validated yet", len(inferences))
	}
	return reached, summary
}

type validationProgress struct {
	required  uint64
	completed uint64
	summary   string
}

func validationProgressFromHostStats(t *testing.T, raw any) validationProgress {
	t.Helper()
	progress := validationProgress{}
	switch stats := raw.(type) {
	case []any:
		for _, rawStat := range stats {
			stat, ok := rawStat.(map[string]any)
			require.True(t, ok, "host_stats entries should be objects")
			progress.add(t, fmt.Sprint(stat["slot_id"]), stat)
		}
	case map[string]any:
		for slotID, rawStat := range stats {
			stat, ok := rawStat.(map[string]any)
			require.True(t, ok, "host_stats[%s] should be an object", slotID)
			progress.add(t, slotID, stat)
		}
	default:
		t.Fatalf("host_stats has unsupported type %T", raw)
	}
	return progress
}

func optionalNumericField(t *testing.T, obj map[string]any, field string) uint64 {
	t.Helper()
	value, ok := obj[field]
	if !ok {
		return 0
	}
	return NumericValue(t, value, field)
}

func (p *validationProgress) add(t *testing.T, slotID string, stat map[string]any) {
	t.Helper()
	required := optionalNumericField(t, stat, "required_validations")
	completed := optionalNumericField(t, stat, "completed_validations")
	p.required += required
	p.completed += completed
	if p.summary != "" {
		p.summary += "; "
	}
	p.summary += fmt.Sprintf("slot %s completed=%d required=%d", slotID, completed, required)
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

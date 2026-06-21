package e2e

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func requireSettlementContract(t *testing.T, settlement map[string]any) {
	t.Helper()
	require.NotEmpty(t, settlement["escrow_id"])
	require.NotEmpty(t, settlement["version"])
	require.NotEmpty(t, settlement["state_root"])
	require.NotZero(t, numericField(t, settlement, "nonce"))

	signatures, ok := settlement["signatures"].([]any)
	require.True(t, ok, "signatures should be a JSON array")
	require.NotEmpty(t, signatures)
	seenSlots := make(map[uint64]struct{}, len(signatures))
	for _, raw := range signatures {
		sig, ok := raw.(map[string]any)
		require.True(t, ok, "signature entries should be objects")
		slotID := numericField(t, sig, "slot_id")
		require.NotEmpty(t, sig["signature"])
		if _, exists := seenSlots[slotID]; exists {
			t.Fatalf("duplicate settlement signature for slot %d", slotID)
		}
		seenSlots[slotID] = struct{}{}
	}
}

func requireCompletedValidationContract(t *testing.T, settlement map[string]any) {
	t.Helper()
	progress := validationProgressFromHostStats(t, settlement["host_stats"])
	require.Greater(t, progress.required, uint64(0), "settlement should require at least one validation")
	require.Equal(t, progress.required, progress.completed,
		"settlement should include every required validation as completed")
}

func requireValidationTargetContract(t *testing.T, settlement map[string]any, target uint64) {
	t.Helper()
	reached, summary := hasHostValidationTarget(t, settlement["host_stats"], target)
	require.Truef(t, reached, "settlement should include a host with %d/%d completed validations: %s",
		target, target, summary)
}

func hasHostValidationTarget(t *testing.T, raw any, target uint64) (bool, string) {
	t.Helper()
	found := false
	summary := ""
	visit := func(slotID string, stat map[string]any) {
		required := numericField(t, stat, "required_validations")
		completed := numericField(t, stat, "completed_validations")
		if summary != "" {
			summary += "; "
		}
		summary += fmt.Sprintf("slot %s completed=%d required=%d", slotID, completed, required)
		if completed == target && required == target {
			found = true
		}
	}

	switch stats := raw.(type) {
	case []any:
		for _, rawStat := range stats {
			stat, ok := rawStat.(map[string]any)
			require.True(t, ok, "host_stats entries should be objects")
			visit(fmt.Sprint(stat["slot_id"]), stat)
		}
	case map[string]any:
		for slotID, rawStat := range stats {
			stat, ok := rawStat.(map[string]any)
			require.True(t, ok, "host_stats[%s] should be an object", slotID)
			visit(slotID, stat)
		}
	default:
		t.Fatalf("host_stats has unsupported type %T", raw)
	}
	return found, summary
}

func validationEvidenceFromState(t *testing.T, state map[string]any) validationProgress {
	t.Helper()
	inferences, ok := state["inferences"].(map[string]any)
	require.True(t, ok, "state inferences should be an object")
	progress := validationProgress{}
	progress.required = uint64(len(inferences))
	for inferenceID, raw := range inferences {
		inference, ok := raw.(map[string]any)
		require.True(t, ok, "state inference %s should be an object", inferenceID)
		validatedBy, ok := inference["validated_by"].([]any)
		if ok && len(validatedBy) > 0 {
			progress.completed += uint64(len(validatedBy))
			if progress.summary != "" {
				progress.summary += "; "
			}
			progress.summary += fmt.Sprintf("inference %s validated_by=%v", inferenceID, validatedBy)
		}
	}
	if progress.summary == "" {
		progress.summary = fmt.Sprintf("%d inferences, none validated yet", len(inferences))
	}
	return progress
}

func hasInferenceValidationTarget(t *testing.T, state map[string]any, target uint64) (bool, string) {
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
			slotID := numericValue(t, rawSlot, "validated_by slot")
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

func (p *validationProgress) add(t *testing.T, slotID string, stat map[string]any) {
	t.Helper()
	required := numericField(t, stat, "required_validations")
	completed := numericField(t, stat, "completed_validations")
	p.required += required
	p.completed += completed
	if p.summary != "" {
		p.summary += "; "
	}
	p.summary += fmt.Sprintf("slot %s completed=%d required=%d", slotID, completed, required)
}

func numericField(t *testing.T, obj map[string]any, field string) uint64 {
	t.Helper()
	value, ok := obj[field]
	require.Truef(t, ok, "missing numeric field %q", field)
	return numericValue(t, value, field)
}

func numericValue(t *testing.T, value any, field string) uint64 {
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

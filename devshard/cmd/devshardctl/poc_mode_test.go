package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func setPoCModeForTest(t *testing.T, mode string) {
	t.Helper()

	pocModeMu.RLock()
	prevMode := currentPoCMode
	prevActive := currentPoCActive
	prevReason := currentPoCReason
	prevGeneration := currentPoCGeneration
	prevLoaded := currentPoCPreservedLoaded
	prevModels := make(map[string]map[string]struct{}, len(currentPoCPreservedModels))
	for model, keys := range currentPoCPreservedModels {
		prevModels[model] = make(map[string]struct{}, len(keys))
		for key := range keys {
			prevModels[model][key] = struct{}{}
		}
	}
	pocModeMu.RUnlock()

	ConfigurePoCRequestMode(mode)
	setPoCPhaseState(false, "")

	t.Cleanup(func() {
		pocModeMu.Lock()
		currentPoCMode = prevMode
		currentPoCActive = prevActive
		currentPoCReason = prevReason
		currentPoCGeneration = prevGeneration
		currentPoCPreservedLoaded = prevLoaded
		currentPoCPreservedModels = prevModels
		pocModeMu.Unlock()
	})
}

func TestShouldUseProbeForParticipantUsesModelPreservedSet(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	setPoCPhaseState(true, "confirmation_poc")
	t.Cleanup(func() { setPoCPreservedParticipantsByModel(nil) })

	setPoCPreservedParticipantsByModel(map[string][]string{
		"Model/A": []string{"participant-a"},
		"Model/B": []string{"participant-b"},
	})

	require.False(t, shouldUseProbeForParticipant("Model/A", "participant-a"))
	require.True(t, shouldUseProbeForParticipant("Model/A", "participant-b"))
	require.False(t, shouldUseProbeForParticipant("Model/B", "participant-b"))
	require.True(t, shouldUseProbeForParticipant("Model/B", "participant-a"))
	require.True(t, shouldUseProbeForParticipant("Model/C", "participant-a"))
}

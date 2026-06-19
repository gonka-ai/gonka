package main

import (
	"testing"
	"time"

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

func resetPoCPhaseStateForTest(t *testing.T) {
	t.Helper()
	setPoCPhaseState(false, "")
	t.Cleanup(func() { setPoCPhaseState(false, "") })
}

func applyRedundancySettingsForTest(t *testing.T, settings RedundancySettings) {
	t.Helper()
	prev := captureRedundancyTimingSettings()
	ApplyRedundancySettings(settings)
	t.Cleanup(func() { restoreRedundancyTimingSettings(prev) })
}

func captureRedundancyTimingSettings() RedundancySettings {
	return RedundancySettings{
		ReceiptTimeoutMS:              int64(ReceiptTimeout / time.Millisecond),
		FirstTokenTimeoutFloorMS:      int64(FirstTokenTimeoutCap / time.Millisecond),
		PerInputTokenFirstTokenLagMS:  int64(PerInputTokenFirstTokenLag / time.Millisecond),
		InterChunkStallTimeoutMS:      int64(InterChunkStallTimeout / time.Millisecond),
		StreamingAttemptHardTimeoutMS: int64(StreamingAttemptHardTimeout / time.Millisecond),
		NonStreamResponseFloorMS:      int64(NonStreamResponseFloor / time.Millisecond),
		NonStreamNoContentTimeoutMS:   int64(nonStreamingNoContentTimeout / time.Millisecond),
		NonStreamMaxAttemptWaitMS:     int64(nonStreamingMaxAttemptWait / time.Millisecond),
		PerInputTokenResponseLagMS:    int64(PerInputTokenResponseLag / time.Millisecond),
		SecondaryWaitAfterWinnerMS:    int64(SecondaryWaitAfterWinner / time.Millisecond),
	}
}

func restoreRedundancyTimingSettings(settings RedundancySettings) {
	ApplyRedundancySettings(settings)
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

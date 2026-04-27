package main

import "testing"

func setPoCModeForTest(t *testing.T, mode string) {
	t.Helper()

	pocModeMu.RLock()
	prevMode := currentPoCMode
	prevActive := currentPoCActive
	prevReason := currentPoCReason
	prevLoaded := currentPoCPreservedLoaded
	prevKeys := make(map[string]struct{}, len(currentPoCPreservedKeys))
	for key := range currentPoCPreservedKeys {
		prevKeys[key] = struct{}{}
	}
	pocModeMu.RUnlock()

	ConfigurePoCRequestMode(mode)
	setPoCPhaseState(false, "")

	t.Cleanup(func() {
		pocModeMu.Lock()
		currentPoCMode = prevMode
		currentPoCActive = prevActive
		currentPoCReason = prevReason
		currentPoCPreservedLoaded = prevLoaded
		currentPoCPreservedKeys = prevKeys
		pocModeMu.Unlock()
	})
}

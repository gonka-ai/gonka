package apiconfig

import (
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// EpochChangeListener runs when ApplyRuntimeConfigBlockIfChanged publishes a new
// snapshot because the chain epoch advanced (not on initial publish or
// param-only updates).
type EpochChangeListener func(oldEpoch, newEpoch uint64)

// SetEpochChangeHandler installs the process-wide callback for epoch transitions.
// Replaces any previous handler. Pass nil to clear.
func (cm *ConfigManager) SetEpochChangeHandler(fn EpochChangeListener) {
	cm.epochOnChangeMu.Lock()
	cm.epochOnChange = fn
	cm.epochOnChangeMu.Unlock()
}

func (cm *ConfigManager) notifyEpochChange(oldEpoch, newEpoch uint64) {
	cm.epochOnChangeMu.Lock()
	fn := cm.epochOnChange
	cm.epochOnChangeMu.Unlock()
	if fn == nil {
		return
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("epoch change handler panicked", types.Config, "panic", r)
			}
		}()
		fn(oldEpoch, newEpoch)
	}()
}

package devshard

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"

	devshardpkg "devshard"
)

// ConfigManagerAvailability implements devshard.AvailabilityProvider for the
// embedded dapi host. It reads the same caches the event listener updates each
// block (no separate AvailabilityTracker on the listener path).
type ConfigManagerAvailability struct {
	cm      *apiconfig.ConfigManager
	tracker *chainphase.ChainPhaseTracker
}

func NewConfigManagerAvailability(cm *apiconfig.ConfigManager, tracker *chainphase.ChainPhaseTracker) *ConfigManagerAvailability {
	return &ConfigManagerAvailability{cm: cm, tracker: tracker}
}

func (a *ConfigManagerAvailability) CurrentAvailability() devshardpkg.AvailabilityStatus {
	if a == nil || a.cm == nil {
		return devshardpkg.AvailabilityStatus{Enabled: true}
	}

	epochID := uint64(0)
	if a.tracker != nil {
		if st := a.tracker.GetCurrentEpochState(); st != nil {
			epochID = st.LatestEpoch.EpochIndex
		}
	}

	snap := a.cm.RuntimeConfigSnapshot(epochID)
	ts := int64(0)
	if !snap.ServedAt.IsZero() {
		ts = snap.ServedAt.Unix()
	}
	return devshardpkg.AvailabilityStatus{
		Enabled: snap.DevshardRequestsEnabled,
		Time:    ts,
		EpochID: epochID,
	}
}

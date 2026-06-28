package apiconfig

import (
	"time"

	"decentralized-api/logging"
	"github.com/productscience/inference/x/inference/types"
)

// runtimeConfigContent is the chain-driven subset of RuntimeConfig used for
// change detection (excludes ParamsBlockHeight and ServedAt).
type runtimeConfigContent struct {
	LogprobsMode            string
	DevshardRequestsEnabled bool
	MaxNonce                uint32
	ApprovedVersions        []DevshardVersion
	RefusalTimeout          int64
	ExecutionTimeout        int64
	ValidationRate          uint32
	VoteThresholdFactor     uint32
}

func runtimeConfigSnapshotFromContent(height int64, epochID uint64, c runtimeConfigContent) RuntimeConfigSnapshot {
	versions := make([]DevshardVersion, len(c.ApprovedVersions))
	copy(versions, c.ApprovedVersions)
	return RuntimeConfigSnapshot{
		ParamsBlockHeight:       height,
		CurrentEpochID:          epochID,
		LogprobsMode:            c.LogprobsMode,
		DevshardRequestsEnabled: c.DevshardRequestsEnabled,
		MaxNonce:                c.MaxNonce,
		ApprovedVersions:        versions,
		ServedAt:                time.Now(),
		RefusalTimeout:          c.RefusalTimeout,
		ExecutionTimeout:        c.ExecutionTimeout,
		ValidationRate:          c.ValidationRate,
		VoteThresholdFactor:     c.VoteThresholdFactor,
	}
}

func (a runtimeConfigContent) equal(b runtimeConfigContent) bool {
	return a.LogprobsMode == b.LogprobsMode &&
		a.DevshardRequestsEnabled == b.DevshardRequestsEnabled &&
		a.MaxNonce == b.MaxNonce &&
		devshardVersionsEqual(a.ApprovedVersions, b.ApprovedVersions) &&
		a.RefusalTimeout == b.RefusalTimeout &&
		a.ExecutionTimeout == b.ExecutionTimeout &&
		a.ValidationRate == b.ValidationRate &&
		a.VoteThresholdFactor == b.VoteThresholdFactor
}

func devshardVersionsEqual(a, b []DevshardVersion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type runtimePublishedMarker struct {
	initialized bool
	epochID     uint64
	content     runtimeConfigContent
}

// ApplyRuntimeConfigBlockIfChanged records params_block_height and notifies
// long-poll waiters only when runtime params content or epochID changed.
// blockHeight is the chain height at which the change was observed.
// Returns true when a new revision was published.
func (cm *ConfigManager) ApplyRuntimeConfigBlockIfChanged(blockHeight int64, epochID uint64) bool {
	content := cm.liveRuntimeConfigContent()

	cm.runtimePublishMu.Lock()
	if cm.runtimePublished.initialized &&
		cm.runtimePublished.epochID == epochID &&
		cm.runtimePublished.content.equal(content) {
		cm.runtimePublishMu.Unlock()
		logging.Debug("runtime_config: publish skipped (content unchanged)", types.Config,
			"blockHeight", blockHeight,
			"epochID", epochID,
			"devshardRequestsEnabled", content.DevshardRequestsEnabled,
			"publishedDevshardRequestsEnabled", cm.runtimePublished.content.DevshardRequestsEnabled,
		)
		return false
	}

	var reason string
	var oldEpoch uint64
	switch {
	case !cm.runtimePublished.initialized:
		reason = "initial_publish"
	case cm.runtimePublished.epochID != epochID:
		oldEpoch = cm.runtimePublished.epochID
		reason = "epoch_change"
	default:
		reason = "param_change"
	}

	if blockHeight > cm.runtimeParamsBlockHeight {
		cm.runtimeParamsBlockHeight = blockHeight
	}

	cm.runtimePublished = runtimePublishedMarker{
		initialized: true,
		epochID:     epochID,
		content:     copyRuntimeConfigContent(content),
	}
	fireEpochChange := reason == "epoch_change"
	epochChangeOld, epochChangeNew := oldEpoch, epochID
	cm.runtimePublishMu.Unlock()

	// Wake long-poll waiters and epoch hooks without holding runtimePublishMu so
	// callbacks may call RuntimeConfigSnapshot without self-deadlock on RWMutex.
	if cm.runtimeConfigNotifier != nil {
		cm.runtimeConfigNotifier.Notify()
	}
	if fireEpochChange {
		cm.notifyEpochChange(epochChangeOld, epochChangeNew)
	}
	logging.Debug("runtime_config: published revision", types.Config,
		"blockHeight", blockHeight,
		"epochID", epochID,
		"reason", reason,
		"maxNonce", content.MaxNonce,
		"devshardRequestsEnabled", content.DevshardRequestsEnabled,
	)
	return true
}

func copyRuntimeConfigContent(c runtimeConfigContent) runtimeConfigContent {
	versions := make([]DevshardVersion, len(c.ApprovedVersions))
	copy(versions, c.ApprovedVersions)
	c.ApprovedVersions = versions
	return c
}

// ResetRuntimePublishedState clears the last-published marker (tests only).
func (cm *ConfigManager) ResetRuntimePublishedState() {
	cm.runtimePublishMu.Lock() // writers: publish and test reset
	defer cm.runtimePublishMu.Unlock()
	cm.runtimePublished = runtimePublishedMarker{}
	cm.runtimeParamsBlockHeight = 0
}

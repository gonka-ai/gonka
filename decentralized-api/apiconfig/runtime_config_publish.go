package apiconfig

import (
	"decentralized-api/logging"
	"github.com/productscience/inference/x/inference/types"
)

// runtimeConfigContent is the chain-driven subset of RuntimeConfig used for
// change detection (excludes ParamsBlockHeight and ServedAt).
type runtimeConfigContent struct {
	LogprobsMode                      string
	DevshardRequestsEnabled           bool
	DefaultSealGraceNonces            uint32
	DefaultInferenceClearGraceSeconds uint32
	MaxNonce                          uint32
	ApprovedVersions                  []DevshardVersion
}

func runtimeConfigContentFromSnapshot(s RuntimeConfigSnapshot) runtimeConfigContent {
	versions := make([]DevshardVersion, len(s.ApprovedVersions))
	copy(versions, s.ApprovedVersions)
	return runtimeConfigContent{
		LogprobsMode:                      s.LogprobsMode,
		DevshardRequestsEnabled:           s.DevshardRequestsEnabled,
		DefaultSealGraceNonces:            s.DefaultSealGraceNonces,
		DefaultInferenceClearGraceSeconds: s.DefaultInferenceClearGraceSeconds,
		MaxNonce:                          s.MaxNonce,
		ApprovedVersions:                  versions,
	}
}

func (a runtimeConfigContent) equal(b runtimeConfigContent) bool {
	return a.LogprobsMode == b.LogprobsMode &&
		a.DevshardRequestsEnabled == b.DevshardRequestsEnabled &&
		a.DefaultSealGraceNonces == b.DefaultSealGraceNonces &&
		a.DefaultInferenceClearGraceSeconds == b.DefaultInferenceClearGraceSeconds &&
		a.MaxNonce == b.MaxNonce &&
		devshardVersionsEqual(a.ApprovedVersions, b.ApprovedVersions)
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
	content := runtimeConfigContentFromSnapshot(cm.RuntimeConfigSnapshot(epochID))

	cm.runtimePublishMu.Lock()
	defer cm.runtimePublishMu.Unlock()

	if cm.runtimePublished.initialized &&
		cm.runtimePublished.epochID == epochID &&
		cm.runtimePublished.content.equal(content) {
		logging.Debug("runtime_config: publish skipped (content unchanged)", types.Config,
			"blockHeight", blockHeight,
			"epochID", epochID,
			"devshardRequestsEnabled", content.DevshardRequestsEnabled,
			"publishedDevshardRequestsEnabled", cm.runtimePublished.content.DevshardRequestsEnabled,
		)
		return false
	}

	var reason string
	switch {
	case !cm.runtimePublished.initialized:
		reason = "initial_publish"
	case cm.runtimePublished.epochID != epochID:
		reason = "epoch_change"
	default:
		reason = "param_change"
	}

	for {
		cur := cm.runtimeParamsBlockHeight.Load()
		if blockHeight <= cur {
			break
		}
		if cm.runtimeParamsBlockHeight.CompareAndSwap(cur, blockHeight) {
			break
		}
	}

	cm.runtimePublished = runtimePublishedMarker{
		initialized: true,
		epochID:     epochID,
		content:     copyRuntimeConfigContent(content),
	}
	if cm.runtimeConfigNotifier != nil {
		cm.runtimeConfigNotifier.Notify()
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
	cm.runtimePublishMu.Lock()
	defer cm.runtimePublishMu.Unlock()
	cm.runtimePublished = runtimePublishedMarker{}
	cm.runtimeParamsBlockHeight.Store(0)
}

package nodemanager

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"common/runtimeconfig"
)

type configSnapshotSource struct {
	cm *apiconfig.ConfigManager
}

func (s configSnapshotSource) RuntimeConfigSnapshot(epochID uint64) runtimeconfig.Snapshot {
	return snapshotFromAPIConfig(s.cm.RuntimeConfigSnapshot(epochID))
}

type phaseEpochSource struct {
	pt *chainphase.ChainPhaseTracker
}

func (p phaseEpochSource) CurrentEpochID() uint64 {
	return currentEpochID(p.pt)
}

type configNotifierAdapter struct {
	cm *apiconfig.ConfigManager
}

func (a configNotifierAdapter) NotifyChan() (<-chan struct{}, bool) {
	n := a.cm.RuntimeConfigNotifier()
	if n == nil {
		return nil, false
	}
	return n.NotifyChan(), true
}

func snapshotFromAPIConfig(snap apiconfig.RuntimeConfigSnapshot) runtimeconfig.Snapshot {
	versions := make([]runtimeconfig.ApprovedVersion, len(snap.ApprovedVersions))
	for i, v := range snap.ApprovedVersions {
		versions[i] = runtimeconfig.ApprovedVersion{
			Name:   v.Name,
			Binary: v.Binary,
			SHA256: v.SHA256,
		}
	}
	thresholds := make([]runtimeconfig.ModelValidationThreshold, len(snap.ModelValidationThresholds))
	for i, t := range snap.ModelValidationThresholds {
		thresholds[i] = runtimeconfig.ModelValidationThreshold{
			ModelID:  t.ModelID,
			Value:    t.Value,
			Exponent: t.Exponent,
		}
	}
	return runtimeconfig.Snapshot{
		ParamsBlockHeight:         snap.ParamsBlockHeight,
		CurrentEpochID:            snap.CurrentEpochID,
		LogprobsMode:              snap.LogprobsMode,
		DevshardRequestsEnabled:   snap.DevshardRequestsEnabled,
		MaxNonce:                  snap.MaxNonce,
		ApprovedVersions:          versions,
		ServedAt:                  snap.ServedAt,
		RefusalTimeout:            snap.RefusalTimeout,
		ExecutionTimeout:          snap.ExecutionTimeout,
		ValidationRate:            snap.ValidationRate,
		VoteThresholdFactor:       snap.VoteThresholdFactor,
		ModelValidationThresholds: thresholds,
	}
}

func newRuntimeConfigServer(cm *apiconfig.ConfigManager, pt *chainphase.ChainPhaseTracker) *runtimeconfig.Server {
	return runtimeconfig.NewServer(runtimeconfig.ServerDeps{
		Source:     configSnapshotSource{cm: cm},
		Epochs:     phaseEpochSource{pt: pt},
		Notifier:   configNotifierAdapter{cm: cm},
		MaxWaitCap: runtimeConfigMaxWaitCap,
	})
}

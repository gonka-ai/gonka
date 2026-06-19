package nodemanager

import (
	"decentralized-api/apiconfig"
	"devshard/nodemanager/gen"
)

func runtimeConfigFromSnapshot(snap apiconfig.RuntimeConfigSnapshot) *gen.RuntimeConfig {
	versions := make([]*gen.ApprovedVersion, len(snap.ApprovedVersions))
	for i, v := range snap.ApprovedVersions {
		versions[i] = &gen.ApprovedVersion{
			Name:   v.Name,
			Binary: v.Binary,
			Sha256: v.SHA256,
		}
	}
	return &gen.RuntimeConfig{
		ParamsBlockHeight:       snap.ParamsBlockHeight,
		CurrentEpochId:          snap.CurrentEpochID,
		LogprobsMode:            snap.LogprobsMode,
		DevshardRequestsEnabled: snap.DevshardRequestsEnabled,
		MaxNonce:                snap.MaxNonce,
		ApprovedVersions:        versions,
		ServedAtUnix:            snap.ServedAt.Unix(),
		RefusalTimeout:          snap.RefusalTimeout,
		ExecutionTimeout:        snap.ExecutionTimeout,
		ValidationRate:          snap.ValidationRate,
		VoteThresholdFactor:     snap.VoteThresholdFactor,
	}
}

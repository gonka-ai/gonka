package runtimeconfig

import (
	"time"

	"devshard/nodemanager/gen"
)

// SnapshotFromProto maps nodemanager.RuntimeConfig to Snapshot.
func SnapshotFromProto(c *gen.RuntimeConfig) Snapshot {
	if c == nil {
		return Snapshot{}
	}
	versions := make([]ApprovedVersion, 0, len(c.GetApprovedVersions()))
	for _, v := range c.GetApprovedVersions() {
		if v == nil {
			continue
		}
		versions = append(versions, ApprovedVersion{
			Name:   v.GetName(),
			Binary: v.GetBinary(),
			SHA256: v.GetSha256(),
		})
	}
	servedAt := time.Unix(c.GetServedAtUnix(), 0)
	if c.GetServedAtUnix() == 0 {
		servedAt = time.Time{}
	}
	return Snapshot{
		ParamsBlockHeight:       c.GetParamsBlockHeight(),
		CurrentEpochID:          c.GetCurrentEpochId(),
		LogprobsMode:            c.GetLogprobsMode(),
		DevshardRequestsEnabled: c.GetDevshardRequestsEnabled(),
		MaxNonce:                c.GetMaxNonce(),
		ApprovedVersions:        versions,
		ServedAt:                servedAt,
		RefusalTimeout:          c.GetRefusalTimeout(),
		ExecutionTimeout:        c.GetExecutionTimeout(),
		ValidationRate:          c.GetValidationRate(),
		VoteThresholdFactor:     c.GetVoteThresholdFactor(),
	}
}

// ProtoFromSnapshot maps Snapshot to nodemanager.RuntimeConfig (tests).
func ProtoFromSnapshot(s Snapshot) *gen.RuntimeConfig {
	versions := make([]*gen.ApprovedVersion, 0, len(s.ApprovedVersions))
	for _, v := range s.ApprovedVersions {
		versions = append(versions, &gen.ApprovedVersion{
			Name:   v.Name,
			Binary: v.Binary,
			Sha256: v.SHA256,
		})
	}
	var servedAt int64
	if !s.ServedAt.IsZero() {
		servedAt = s.ServedAt.Unix()
	}
	return &gen.RuntimeConfig{
		ParamsBlockHeight:       s.ParamsBlockHeight,
		CurrentEpochId:          s.CurrentEpochID,
		LogprobsMode:            s.LogprobsMode,
		DevshardRequestsEnabled: s.DevshardRequestsEnabled,
		MaxNonce:                s.MaxNonce,
		ApprovedVersions:        versions,
		ServedAtUnix:            servedAt,
		RefusalTimeout:          s.RefusalTimeout,
		ExecutionTimeout:        s.ExecutionTimeout,
		ValidationRate:          s.ValidationRate,
		VoteThresholdFactor:     s.VoteThresholdFactor,
	}
}

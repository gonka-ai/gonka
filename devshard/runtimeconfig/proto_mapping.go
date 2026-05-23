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
		ParamsBlockHeight:                 c.GetParamsBlockHeight(),
		CurrentEpochID:                    c.GetCurrentEpochId(),
		LogprobsMode:                      c.GetLogprobsMode(),
		DevshardRequestsEnabled:           c.GetDevshardRequestsEnabled(),
		DefaultSealGraceNonces:            c.GetDefaultSealGraceNonces(),
		DefaultInferenceClearGraceSeconds: c.GetDefaultInferenceClearGraceSeconds(),
		MaxNonce:                          c.GetMaxNonce(),
		ApprovedVersions:                  versions,
		ServedAt:                          servedAt,
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
		ParamsBlockHeight:                 s.ParamsBlockHeight,
		CurrentEpochId:                    s.CurrentEpochID,
		LogprobsMode:                      s.LogprobsMode,
		DevshardRequestsEnabled:           s.DevshardRequestsEnabled,
		DefaultSealGraceNonces:            s.DefaultSealGraceNonces,
		DefaultInferenceClearGraceSeconds: s.DefaultInferenceClearGraceSeconds,
		MaxNonce:                          s.MaxNonce,
		ApprovedVersions:                  versions,
		ServedAtUnix:                      servedAt,
	}
}

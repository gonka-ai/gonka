package client

import (
	"time"

	"common/nodemanager/gen"
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
	var servedAt time.Time
	// 0 = unset (canonical). Negative values are treated as unset too so a
	// legacy ToProto that emitted time.Time{}.Unix() (~year 1) does not
	// look like a real served-at timestamp.
	if unix := c.GetServedAtUnix(); unix > 0 {
		servedAt = time.Unix(unix, 0)
	}
	var thresholds []ModelValidationThreshold
	for _, t := range c.GetValidationThresholds() {
		if t == nil {
			continue
		}
		thresholds = append(thresholds, ModelValidationThreshold{
			ModelID:  t.GetModelId(),
			Value:    t.GetThresholdValue(),
			Exponent: t.GetThresholdExponent(),
		})
	}
	return Snapshot{
		ParamsBlockHeight:         c.GetParamsBlockHeight(),
		CurrentEpochID:            c.GetCurrentEpochId(),
		LogprobsMode:              c.GetLogprobsMode(),
		DevshardRequestsEnabled:   c.GetDevshardRequestsEnabled(),
		MaxNonce:                  c.GetMaxNonce(),
		ApprovedVersions:          versions,
		ServedAt:                  servedAt,
		RefusalTimeout:            c.GetRefusalTimeout(),
		ExecutionTimeout:          c.GetExecutionTimeout(),
		ValidationRate:            c.GetValidationRate(),
		VoteThresholdFactor:       c.GetVoteThresholdFactor(),
		ModelValidationThresholds: thresholds,
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
	thresholds := make([]*gen.ModelValidationThreshold, 0, len(s.ModelValidationThresholds))
	for _, t := range s.ModelValidationThresholds {
		thresholds = append(thresholds, &gen.ModelValidationThreshold{
			ModelId:           t.ModelID,
			ThresholdValue:    t.Value,
			ThresholdExponent: t.Exponent,
		})
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
		ValidationThresholds:    thresholds,
	}
}

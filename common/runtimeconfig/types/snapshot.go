// Package types holds transport-agnostic runtime-config snapshot types shared by
// the NodeManager server, client providers, and chain/mock fetchers. It depends
// only on nodemanager proto types — not on common/chain or inference-chain —
// so client tests avoid linking Cosmos/keyring (and Linux D-Bus) via goleak.
package types

import (
	"time"

	"common/nodemanager/gen"
)

// ApprovedVersion mirrors gen.ApprovedVersion without exposing the proto type
// to snapshot producers.
type ApprovedVersion struct {
	Name   string
	Binary string
	SHA256 string
}

// ModelValidationThreshold mirrors gen.ModelValidationThreshold: a per-model
// inference validation threshold encoded as Value * 10^Exponent (cosmos
// LegacyDec coefficient/exponent) to preserve exactness over the wire.
type ModelValidationThreshold struct {
	ModelID  string
	Value    int64
	Exponent int32
}

// Snapshot is the transport-agnostic view of chain-driven runtime params served
// to devshardd via GetRuntimeConfig. Its fields mirror gen.RuntimeConfig 1:1 so
// ToProto is a straight copy.
type Snapshot struct {
	// ParamsBlockHeight is the chain block height at which the last published
	// runtime revision was recorded. It advances with the published content
	// snapshot; cache writes alone do not move it.
	ParamsBlockHeight         int64
	CurrentEpochID            uint64
	LogprobsMode              string
	DevshardRequestsEnabled   bool
	MaxNonce                  uint32
	ApprovedVersions          []ApprovedVersion
	ServedAt                  time.Time
	RefusalTimeout            int64
	ExecutionTimeout          int64
	ValidationRate            uint32
	VoteThresholdFactor       uint32
	ModelValidationThresholds []ModelValidationThreshold
}

// ToProto converts the snapshot to the wire type. Equivalent to the dapi
// runtimeConfigFromSnapshot it replaces.
func (s Snapshot) ToProto() *gen.RuntimeConfig {
	versions := make([]*gen.ApprovedVersion, len(s.ApprovedVersions))
	for i, v := range s.ApprovedVersions {
		versions[i] = &gen.ApprovedVersion{
			Name:   v.Name,
			Binary: v.Binary,
			Sha256: v.SHA256,
		}
	}
	thresholds := make([]*gen.ModelValidationThreshold, len(s.ModelValidationThresholds))
	for i, t := range s.ModelValidationThresholds {
		thresholds[i] = &gen.ModelValidationThreshold{
			ModelId:           t.ModelID,
			ThresholdValue:    t.Value,
			ThresholdExponent: t.Exponent,
		}
	}
	var servedAtUnix int64
	if !s.ServedAt.IsZero() {
		servedAtUnix = s.ServedAt.Unix()
	}
	return &gen.RuntimeConfig{
		ParamsBlockHeight:       s.ParamsBlockHeight,
		CurrentEpochId:          s.CurrentEpochID,
		LogprobsMode:            s.LogprobsMode,
		DevshardRequestsEnabled: s.DevshardRequestsEnabled,
		MaxNonce:                s.MaxNonce,
		ApprovedVersions:        versions,
		ServedAtUnix:            servedAtUnix,
		RefusalTimeout:          s.RefusalTimeout,
		ExecutionTimeout:        s.ExecutionTimeout,
		ValidationRate:          s.ValidationRate,
		VoteThresholdFactor:     s.VoteThresholdFactor,
		ValidationThresholds:    thresholds,
	}
}

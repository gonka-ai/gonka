package apiconfig

import "time"

// RuntimeConfigSnapshot is the immutable view of chain-driven runtime params
// served to devshardd via NodeManager GetRuntimeConfig gRPC.
type RuntimeConfigSnapshot struct {
	// ParamsBlockHeight is the chain block height at which the last published runtime
	// revision was recorded (see ApplyRuntimeConfigBlockIfChanged). It advances together
	// with the published content snapshot; cache writes alone do not move it.
	ParamsBlockHeight       int64
	CurrentEpochID          uint64
	LogprobsMode            string
	DevshardRequestsEnabled bool
	MaxNonce                uint32
	ApprovedVersions        []DevshardVersion
	ServedAt                time.Time
	RefusalTimeout          int64
	ExecutionTimeout        int64
	ValidationRate          uint32
	VoteThresholdFactor     uint32
}

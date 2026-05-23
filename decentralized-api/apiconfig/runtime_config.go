package apiconfig

import "time"

// RuntimeConfigSnapshot is the immutable view of chain-driven runtime params
// served to devshardd via NodeManager GetRuntimeConfig gRPC.
type RuntimeConfigSnapshot struct {
	// ParamsBlockHeight is the chain block height at which runtime params or epoch
	// last changed (see ApplyRuntimeConfigBlockIfChanged). Used for gRPC change
	// detection; ordinary blocks without a change do not advance it.
	ParamsBlockHeight                 int64
	CurrentEpochID                    uint64
	LogprobsMode                      string
	DevshardRequestsEnabled           bool
	DefaultSealGraceNonces            uint32
	DefaultInferenceClearGraceSeconds uint32
	MaxNonce                          uint32
	ApprovedVersions                  []DevshardVersion
	ServedAt                          time.Time
}

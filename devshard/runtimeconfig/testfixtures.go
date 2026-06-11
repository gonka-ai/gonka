package runtimeconfig

import (
	"time"

	"devshard/nodemanager/gen"
)

// TestRuntimeConfigProto builds a RuntimeConfig proto for server stubs.
func TestRuntimeConfigProto(height int64, epoch uint64, logprobs string) *gen.RuntimeConfig {
	return ProtoFromSnapshot(Snapshot{
		ParamsBlockHeight:       height,
		CurrentEpochID:          epoch,
		LogprobsMode:            logprobs,
		DevshardRequestsEnabled: true,
		MaxNonce:                100,
		ApprovedVersions: []ApprovedVersion{
			{Name: "v1", Binary: "/bin/v1", SHA256: "abc"},
		},
		ServedAt: time.Unix(1_700_000_000, 0),
	})
}

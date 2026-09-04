package runtimeconfig

import "common/runtimeconfig/types"

// Snapshot and ApprovedVersion are defined in types/ so client providers can
// import them without linking common/chain or inference-chain.
type (
	Snapshot                 = types.Snapshot
	ApprovedVersion          = types.ApprovedVersion
	ModelValidationThreshold = types.ModelValidationThreshold
	HeightSyncParams         = types.HeightSyncParams
)

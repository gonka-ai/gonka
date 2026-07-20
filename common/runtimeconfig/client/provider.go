package client

import "common/runtimeconfig/types"

// Snapshot and ApprovedVersion alias the transport-agnostic types (types/
// only — no chain/cosmos imports in this package).
type Snapshot = types.Snapshot
type ApprovedVersion = types.ApprovedVersion
type ModelValidationThreshold = types.ModelValidationThreshold

// EpochChangeListener fires once per CurrentEpochID transition observed by the
// provider after the first successful apply.
type EpochChangeListener func(old, new uint64)

// Provider is the surface engine/validation/storage code consumes instead of
// going to chain.
type Provider interface {
	Snapshot() Snapshot
	LogprobsMode() string
	CurrentEpochID() uint64
	Availability() AvailabilityView
	OnEpochChange(EpochChangeListener) (cancel func())
}

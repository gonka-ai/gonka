package runtimeconfig

import (
	"time"

	devshardpkg "devshard"
)

// ApprovedVersion mirrors nodemanager.ApprovedVersion.
type ApprovedVersion struct {
	Name   string
	Binary string
	SHA256 string
}

// Snapshot is an immutable view of dapi's cached chain runtime params.
type Snapshot struct {
	ParamsBlockHeight       int64
	CurrentEpochID          uint64
	LogprobsMode            string
	DevshardRequestsEnabled bool
	MaxNonce                uint32
	ApprovedVersions        []ApprovedVersion
	ServedAt                time.Time
	RefusalTimeout          int64
	ExecutionTimeout        int64
	ValidationRate          uint32
	VoteThresholdFactor     uint32
}

// EpochChangeListener fires once per CurrentEpochID transition observed by the
// provider after the first successful apply from dapi.
type EpochChangeListener func(old, new uint64)

// Provider is the surface engine/validation/storage code consumes instead of
// going to chain.
type Provider interface {
	// Snapshot returns the latest immutable runtime-config snapshot.
	Snapshot() Snapshot
	// LogprobsMode returns the current logprobs mode from Snapshot().
	LogprobsMode() string
	// CurrentEpochID returns the latest observed chain epoch id from Snapshot().
	CurrentEpochID() uint64
	// Availability derives the current availability state from Snapshot().
	Availability() devshardpkg.AvailabilityStatus
	// OnEpochChange registers a listener called once per epoch transition and
	// returns a cancel function that unregisters it.
	OnEpochChange(EpochChangeListener) (cancel func())
}

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
	ParamsBlockHeight                 int64
	CurrentEpochID                    uint64
	LogprobsMode                      string
	DevshardRequestsEnabled           bool
	DefaultSealGraceNonces            uint32
	DefaultInferenceClearGraceSeconds uint32
	MaxNonce                          uint32
	ApprovedVersions                  []ApprovedVersion
	ServedAt                          time.Time
}

// EpochChangeListener fires once per CurrentEpochID transition observed by the
// provider after the first successful apply from dapi.
type EpochChangeListener func(old, new uint64)

// Provider is the surface engine/validation/storage code consumes instead of
// going to chain.
type Provider interface {
	Snapshot() Snapshot
	LogprobsMode() string
	CurrentEpochID() uint64
	Availability() devshardpkg.AvailabilityStatus
	OnEpochChange(EpochChangeListener) (cancel func())
}

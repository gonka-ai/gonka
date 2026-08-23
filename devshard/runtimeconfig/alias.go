package runtimeconfig

import (
	"time"

	commrc "common/runtimeconfig"
	rcclient "common/runtimeconfig/client"
)

// Re-export common snapshot types and the shared client implementation.
type (
	Snapshot            = commrc.Snapshot
	ApprovedVersion     = commrc.ApprovedVersion
	EpochChangeListener = rcclient.EpochChangeListener
	ChainParamsFetcher  = rcclient.ChainParamsFetcher
	Config              = rcclient.Config
	ChainConfig         = rcclient.ChainConfig
	AdaptiveConfig      = rcclient.AdaptiveConfig
	Provider            = rcclient.Provider
	AdaptiveProvider    = rcclient.AdaptiveProvider
	Clock               = rcclient.Clock
	AvailabilitySink    = rcclient.AvailabilitySink
	AvailabilityView    = rcclient.AvailabilityView
)

const (
	SourceActiveGRPC  = rcclient.SourceActiveGRPC
	SourceActiveChain = rcclient.SourceActiveChain
)

var (
	New                 = rcclient.New
	NewChain            = rcclient.NewChain
	NewAdaptive         = rcclient.NewAdaptive
	SnapshotFromProto   = rcclient.SnapshotFromProto
	ProtoFromSnapshot   = rcclient.ProtoFromSnapshot
	TestRuntimeConfigProto = rcclient.TestRuntimeConfigProto
)

func DefaultGRPCStaleSeconds() time.Duration     { return rcclient.DefaultGRPCStaleSeconds() }
func DefaultGRPCReprobeSeconds() time.Duration   { return rcclient.DefaultGRPCReprobeSeconds() }
func DefaultFailbackProbes() int                 { return rcclient.DefaultFailbackProbes() }
func DefaultAdaptiveProbeTimeout() time.Duration { return rcclient.DefaultAdaptiveProbeTimeout() }
func DefaultStaleCheckInterval() time.Duration   { return rcclient.DefaultStaleCheckInterval() }

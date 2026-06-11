package devshard

import (
	"decentralized-api/apiconfig"
	"devshard/runtimeconfig"
)

// RuntimeConfigSnapshotSource is the narrow surface needed for runtime params
// and max-nonce reads (epochParamsProvider in devshardd is smaller than
// runtimeconfig.Provider).
type RuntimeConfigSnapshotSource interface {
	Snapshot() runtimeconfig.Snapshot
}

// SessionParams is the narrow surface HostManager.create needs from the live
// long-poll snapshot at session bind. Fields are deliberately the union of the
// values that will be sourced from chain governance over the lifetime of the
// session-config-flow-plan: zero means "not provided" so callers can fall
// through to the compiled defaults baked into SessionConfig.
//
// All fields are populated from apiconfig.DevshardVersionsCache /
// runtimeconfig.Snapshot (long-poll). Zero means "not provided" at bind time.
type SessionParams struct {
	RefusalTimeout      int64
	ExecutionTimeout    int64
	ValidationRate      uint32
	VoteThresholdFactor uint32 // percent, e.g. 50 == 50%
	MaxNonce            uint32
}

// RuntimeParamsProvider returns the live, long-poll-backed view of
// chain-governance session parameters. Implementations are expected to be
// cheap and lock-free for readers (the underlying caches already are).
type RuntimeParamsProvider interface {
	SessionParams() SessionParams
}

// configManagerRuntimeParams wraps dapi-embedded apiconfig.ConfigManager.
type configManagerRuntimeParams struct {
	cm *apiconfig.ConfigManager
}

// ConfigManagerRuntimeParams returns a RuntimeParamsProvider backed by dapi's
// DevshardVersionsCache (same source as GetRuntimeConfig long-poll).
func ConfigManagerRuntimeParams(cm *apiconfig.ConfigManager) RuntimeParamsProvider {
	return configManagerRuntimeParams{cm: cm}
}

func (p configManagerRuntimeParams) SessionParams() SessionParams {
	cache := p.cm.GetDevshardVersions()
	return SessionParams{
		RefusalTimeout:      cache.RefusalTimeout,
		ExecutionTimeout:    cache.ExecutionTimeout,
		ValidationRate:      cache.ValidationRate,
		VoteThresholdFactor: cache.VoteThresholdFactor,
		MaxNonce:            cache.MaxNonce,
	}
}

// runtimeConfigRuntimeParams wraps the devshardd standalone long-poll snapshot
// source. Mirrors RuntimeConfigMaxNonce — same underlying cache.
type runtimeConfigRuntimeParams struct {
	source RuntimeConfigSnapshotSource
}

// RuntimeConfigRuntimeParams returns a RuntimeParamsProvider backed by
// devshardd's runtime config snapshot (long-poll fed).
func RuntimeConfigRuntimeParams(source RuntimeConfigSnapshotSource) RuntimeParamsProvider {
	return runtimeConfigRuntimeParams{source: source}
}

func (p runtimeConfigRuntimeParams) SessionParams() SessionParams {
	snap := p.source.Snapshot()
	return SessionParams{
		RefusalTimeout:      snap.RefusalTimeout,
		ExecutionTimeout:    snap.ExecutionTimeout,
		ValidationRate:      snap.ValidationRate,
		VoteThresholdFactor: snap.VoteThresholdFactor,
		MaxNonce:            snap.MaxNonce,
	}
}

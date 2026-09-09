package runtimeparams

import (
	devshardpkg "devshard"
	"devshard/runtimeconfig"
)

// SnapshotSource is the narrow surface needed for runtime session params.
type SnapshotSource interface {
	Snapshot() runtimeconfig.Snapshot
}

// Provider returns the live long-poll-backed view of operational governance
// parameters (lane C). Consensus fields come from the escrow row at bind.
type Provider interface {
	SessionParams() SessionParams
}

// SessionParams carries operational governance fields from the long-poll snapshot.
type SessionParams struct {
	RefusalTimeout   int64
	ExecutionTimeout int64
	MaxNonce         uint32
}

type snapshotProvider struct {
	source SnapshotSource
}

// FromSnapshot returns a Provider backed by a runtimeconfig snapshot source.
func FromSnapshot(source SnapshotSource) Provider {
	return snapshotProvider{source: source}
}

func (p snapshotProvider) SessionParams() SessionParams {
	snap := p.source.Snapshot()
	return SessionParams{
		RefusalTimeout:   snap.RefusalTimeout,
		ExecutionTimeout: snap.ExecutionTimeout,
		MaxNonce:         snap.MaxNonce,
	}
}

type maxNonceProvider struct {
	source SnapshotSource
}

// MaxNonceFromSnapshot wraps a runtime snapshot for host max_nonce enforcement.
func MaxNonceFromSnapshot(source SnapshotSource) devshardpkg.MaxNonceProvider {
	return maxNonceProvider{source: source}
}

func (p maxNonceProvider) MaxNonce() uint32 {
	return p.source.Snapshot().MaxNonce
}

package devshard

import (
	devshardpkg "devshard"
)

type runtimeConfigMaxNonce struct {
	source RuntimeConfigSnapshotSource
}

func (s runtimeConfigMaxNonce) MaxNonce() uint32 {
	return s.source.Snapshot().MaxNonce
}

// RuntimeConfigMaxNonce wraps the devshardd long-poll runtime config provider.
func RuntimeConfigMaxNonce(source RuntimeConfigSnapshotSource) devshardpkg.MaxNonceProvider {
	return runtimeConfigMaxNonce{source: source}
}

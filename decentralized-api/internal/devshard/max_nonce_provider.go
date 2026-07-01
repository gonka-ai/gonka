package devshard

import (
	"decentralized-api/apiconfig"
	devshardpkg "devshard"
)

type configManagerMaxNonce struct {
	cm *apiconfig.ConfigManager
}

func (s configManagerMaxNonce) MaxNonce() uint32 {
	return s.cm.GetDevshardVersions().MaxNonce
}

// ConfigManagerMaxNonce wraps dapi's devshard versions cache.
func ConfigManagerMaxNonce(cm *apiconfig.ConfigManager) devshardpkg.MaxNonceProvider {
	return configManagerMaxNonce{cm: cm}
}

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

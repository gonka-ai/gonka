package devshard

import (
	"decentralized-api/apiconfig"
	"devshard/bridge"
	"devshard/runtimeconfig"
)

// ConfigManagerDefaults reads bind grace from dapi's DevshardVersionsCache
// (same source as GetRuntimeConfig).
type ConfigManagerDefaults struct {
	cm *apiconfig.ConfigManager
}

func NewConfigManagerDefaults(cm *apiconfig.ConfigManager) bridge.DevshardDefaults {
	return &ConfigManagerDefaults{cm: cm}
}

func (d *ConfigManagerDefaults) DefaultSealGraceNonces() uint32 {
	return d.cm.GetDevshardVersions().DefaultSealGraceNonces
}

func (d *ConfigManagerDefaults) DefaultInferenceClearGraceSeconds() uint32 {
	return d.cm.GetDevshardVersions().DefaultInferenceClearGraceSeconds
}

// RuntimeConfigSnapshotSource is the narrow surface needed for bind-time grace
// (epochParamsProvider in devshardd is smaller than runtimeconfig.Provider).
type RuntimeConfigSnapshotSource interface {
	Snapshot() runtimeconfig.Snapshot
}

// RuntimeConfigDefaults reads bind grace from the devshardd long-poll snapshot.
type RuntimeConfigDefaults struct {
	source RuntimeConfigSnapshotSource
}

func NewRuntimeConfigDefaults(source RuntimeConfigSnapshotSource) bridge.DevshardDefaults {
	return &RuntimeConfigDefaults{source: source}
}

func (d *RuntimeConfigDefaults) DefaultSealGraceNonces() uint32 {
	return d.source.Snapshot().DefaultSealGraceNonces
}

func (d *RuntimeConfigDefaults) DefaultInferenceClearGraceSeconds() uint32 {
	return d.source.Snapshot().DefaultInferenceClearGraceSeconds
}

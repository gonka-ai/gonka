package apiconfig

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

const kvKeyDevshardVersionOverrides = "devshard_version_overrides"

// DevshardBinary identifies an archive by its exact URL and SHA-256 checksum.
type DevshardBinary struct {
	Binary string `json:"binary"`
	SHA256 string `json:"sha256"`
}

func (b DevshardBinary) Validate() error {
	u, err := url.Parse(b.Binary)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("binary must be an absolute HTTP or HTTPS URL")
	}
	if hash, err := hex.DecodeString(b.SHA256); err != nil || len(hash) != 32 {
		return fmt.Errorf("sha256 must contain 64 hexadecimal characters")
	}
	return nil
}

type DevshardVersionOverride struct {
	From DevshardBinary `json:"from"`
	To   DevshardBinary `json:"to"`
}

func (o DevshardVersionOverride) Validate() error {
	if err := o.From.Validate(); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := o.To.Validate(); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	return nil
}

func sameDevshardBinary(a, b DevshardBinary) bool {
	return a.Binary == b.Binary && strings.EqualFold(a.SHA256, b.SHA256)
}

func (cm *ConfigManager) GetDevshardVersionOverrides() []DevshardVersionOverride {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	return append([]DevshardVersionOverride{}, cm.devshardVersionOverrides...)
}

func (cm *ConfigManager) SetDevshardVersionOverride(ctx context.Context, override DevshardVersionOverride) error {
	if err := override.Validate(); err != nil {
		return err
	}
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	next := append([]DevshardVersionOverride{}, cm.devshardVersionOverrides...)
	for i, existing := range next {
		if sameDevshardBinary(existing.From, override.From) {
			next[i] = override
			return cm.saveDevshardVersionOverrides(ctx, next)
		}
	}
	return cm.saveDevshardVersionOverrides(ctx, append(next, override))
}

func (cm *ConfigManager) DeleteDevshardVersionOverride(ctx context.Context, from DevshardBinary) error {
	if err := from.Validate(); err != nil {
		return err
	}
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	next := make([]DevshardVersionOverride, 0, len(cm.devshardVersionOverrides))
	for _, override := range cm.devshardVersionOverrides {
		if !sameDevshardBinary(override.From, from) {
			next = append(next, override)
		}
	}
	return cm.saveDevshardVersionOverrides(ctx, next)
}

// Caller holds cm.mutex. Publish only after the write succeeds.
func (cm *ConfigManager) saveDevshardVersionOverrides(ctx context.Context, overrides []DevshardVersionOverride) error {
	if cm.sqlDb == nil {
		return fmt.Errorf("config database is unavailable")
	}
	if err := KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyDevshardVersionOverrides, overrides); err != nil {
		return err
	}
	cm.devshardVersionOverrides = overrides
	return nil
}

// GetEffectiveDevshardVersions applies one replacement to each original chain
// entry. The chain cache and runtime config remain unchanged.
func (cm *ConfigManager) GetEffectiveDevshardVersions() DevshardVersionsCache {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	cache := cm.currentConfig.DevshardVersionsCache
	cache.Versions = append([]DevshardVersion{}, cache.Versions...)
	for i, version := range cache.Versions {
		sha := version.SHA256
		if sha == "" {
			if u, err := url.Parse(version.Binary); err == nil {
				checksum := u.Query().Get("checksum")
				if strings.HasPrefix(checksum, "sha256:") {
					sha = strings.TrimPrefix(checksum, "sha256:")
				}
			}
		}
		for _, override := range cm.devshardVersionOverrides {
			if sameDevshardBinary(override.From, DevshardBinary{Binary: version.Binary, SHA256: sha}) {
				cache.Versions[i].Binary = override.To.Binary
				cache.Versions[i].SHA256 = override.To.SHA256
				break
			}
		}
	}
	return cache
}

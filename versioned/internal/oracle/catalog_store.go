package oracle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CatalogStore is versiond's durable last-known-good governance catalog.
// A catalog is committed here before its desired state reaches the process
// manager, so a restart cannot forget a newer revision that was already acted on.
type CatalogStore struct {
	mu      sync.RWMutex
	path    string
	current VersionConfig
	loaded  bool
}

func OpenCatalogStore(path string) (*CatalogStore, error) {
	store := &CatalogStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read catalog snapshot: %w", err)
	}
	var cfg VersionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode catalog snapshot: %w", err)
	}
	if err := validateCatalog(cfg); err != nil {
		return nil, fmt.Errorf("validate catalog snapshot: %w", err)
	}
	store.current = cloneCatalog(cfg)
	store.loaded = true
	return store, nil
}

func (s *CatalogStore) Current() (VersionConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return VersionConfig{}, false
	}
	return cloneCatalog(s.current), true
}

// Accept validates monotonic progression and durably commits candidate. The
// returned catalog is always the current accepted snapshot. changed is false
// for an identical replay of the same revision.
func (s *CatalogStore) Accept(candidate VersionConfig) (current VersionConfig, changed bool, err error) {
	if err := validateCatalog(candidate); err != nil {
		return VersionConfig{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loaded {
		switch {
		case candidate.Revision < s.current.Revision:
			return VersionConfig{}, false, fmt.Errorf(
				"catalog revision regressed from %d to %d",
				s.current.Revision, candidate.Revision,
			)
		case candidate.Revision == s.current.Revision:
			if !catalogContentEqual(s.current, candidate) {
				return VersionConfig{}, false, fmt.Errorf(
					"catalog revision %d changed in place", candidate.Revision,
				)
			}
			return cloneCatalog(s.current), false, nil
		}
		if removed := firstRemovedVersion(s.current, candidate); removed != "" {
			return VersionConfig{}, false, fmt.Errorf(
				"catalog revision %d removed version %q",
				candidate.Revision, removed,
			)
		}
	}

	candidate = cloneCatalog(candidate)
	if err := writeCatalogAtomically(s.path, candidate); err != nil {
		return VersionConfig{}, false, err
	}
	s.current = candidate
	s.loaded = true
	return cloneCatalog(candidate), true, nil
}

func catalogContentEqual(a, b VersionConfig) bool {
	if a.Schema != b.Schema || a.Initialized != b.Initialized || len(a.Versions) != len(b.Versions) {
		return false
	}
	versions := make(map[string]Version, len(a.Versions))
	for _, version := range a.Versions {
		versions[version.Name] = version
	}
	for _, version := range b.Versions {
		if versions[version.Name] != version {
			return false
		}
	}
	return true
}

func firstRemovedVersion(current, candidate VersionConfig) string {
	next := make(map[string]struct{}, len(candidate.Versions))
	for _, version := range candidate.Versions {
		next[version.Name] = struct{}{}
	}
	for _, version := range current.Versions {
		if _, ok := next[version.Name]; !ok {
			return version.Name
		}
	}
	return ""
}

func cloneCatalog(cfg VersionConfig) VersionConfig {
	cfg.Versions = append([]Version(nil), cfg.Versions...)
	return cfg
}

func writeCatalogAtomically(path string, cfg VersionConfig) (retErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create catalog directory: %w", err)
	}
	tmp, err := os.CreateTemp(directory, ".catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("create catalog snapshot: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect catalog snapshot: %w", err)
	}
	if err := json.NewEncoder(tmp).Encode(cfg); err != nil {
		return fmt.Errorf("encode catalog snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync catalog snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close catalog snapshot: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish catalog snapshot: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open catalog directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync catalog directory: %w", err)
	}
	return nil
}

package mockdapi

import (
	"context"
	"sync"

	cosrv "devshard/chainoracle/server"
)

type versionStore struct {
	mu       sync.RWMutex
	versions []cosrv.Version
}

func newVersionStore(versions []cosrv.Version) *versionStore {
	return &versionStore{versions: cloneVersions(versions)}
}

func (s *versionStore) Versions(context.Context) ([]cosrv.Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneVersions(s.versions), nil
}

func (s *versionStore) Set(versions []cosrv.Version) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions = cloneVersions(versions)
}

func cloneVersions(in []cosrv.Version) []cosrv.Version {
	if len(in) == 0 {
		return nil
	}
	out := make([]cosrv.Version, len(in))
	copy(out, in)
	return out
}

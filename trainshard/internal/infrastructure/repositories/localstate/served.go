package localstate

import (
	"context"
	"path/filepath"
	"time"

	"trainshard/internal/domain/shared/ports"
)

type served struct {
	store *Store
	clock ports.Clock
	ttl   time.Duration
}

// First writes before it answers, so a request that is being served cannot be served again by a
// daemon that restarts mid-shell. Ids older than a signature's life are dropped: a repeat that old
// is already turned away by its timestamp
func (s served) First(_ context.Context, request string) (bool, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	spent := map[string]time.Time{}
	if _, err := s.store.readFile(s.path(), &spent); err != nil {
		return false, err
	}
	now := s.clock.Now()
	for id, at := range spent {
		if now.Sub(at) > s.ttl {
			delete(spent, id)
		}
	}
	if _, found := spent[request]; found {
		return false, nil
	}
	spent[request] = now
	return true, s.store.writeFile(s.path(), spent)
}

func (s served) path() string { return filepath.Join(s.store.dir, "served.json") }

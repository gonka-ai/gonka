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
}

// an id is kept until its signature stops passing, not for a span from arrival: a caller whose clock
// runs ahead is admitted early, and its signature outlives any such span
func (s served) First(_ context.Context, request string, until time.Time) (bool, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	spent := map[string]time.Time{}
	if _, err := s.store.readFile(s.path(), &spent); err != nil {
		return false, err
	}
	now := s.clock.Now()
	for id, staleAfter := range spent {
		if now.After(staleAfter) {
			delete(spent, id)
		}
	}
	if _, found := spent[request]; found {
		return false, nil
	}
	spent[request] = until
	return true, s.store.writeFile(s.path(), spent)
}

func (s served) path() string { return filepath.Join(s.store.dir, "served.json") }

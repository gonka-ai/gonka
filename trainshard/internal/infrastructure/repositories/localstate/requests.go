package localstate

import (
	"context"
	"path/filepath"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type requests struct {
	store *Store
	clock ports.Clock
	ttl   time.Duration
}

func (r requests) Result(_ context.Context, id vo.RequestID) ([]run.NodeResult, bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	answered, err := r.load()
	if err != nil {
		return nil, false, err
	}
	recorded, found := answered[string(id)]
	if !found || r.clock.Now().Sub(recorded.At) > r.ttl {
		return nil, false, nil
	}
	return toNodeResults(recorded.Results), true, nil
}

func (r requests) Record(_ context.Context, id vo.RequestID, results []run.NodeResult) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	answered, err := r.load()
	if err != nil {
		return err
	}

	now := r.clock.Now()
	for recorded, entry := range answered {
		if now.Sub(entry.At) > r.ttl {
			delete(answered, recorded)
		}
	}
	answered[string(id)] = requestEntry{Results: fromNodeResults(results), At: now}
	return r.store.writeFile(r.path(), answered)
}

func (r requests) load() (map[string]requestEntry, error) {
	answered := map[string]requestEntry{}
	if _, err := r.store.readFile(r.path(), &answered); err != nil {
		return nil, err
	}
	return answered, nil
}

func (r requests) path() string { return filepath.Join(r.store.dir, "requests.json") }

package localstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Store struct {
	mu  sync.Mutex
	dir string
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Runs() run.RunStore { return runs{store: s} }

func (s *Store) Mesh() mesh.Store { return meshes{store: s} }

func (s *Store) Requests(clock ports.Clock, ttl time.Duration) run.RequestLog {
	return requests{store: s, clock: clock, ttl: ttl}
}

func (s *Store) read(node vo.NodeRef) (nodeFile, bool, error) {
	var file nodeFile
	found, err := s.readFile(s.path(node), &file)
	return file, found, err
}

func (s *Store) write(node vo.NodeRef, file nodeFile) error {
	return s.writeFile(s.path(node), file)
}

func (s *Store) readFile(path string, into any) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return false, fmt.Errorf("state file %q: %w", path, err)
	}
	return true, nil
}

func (s *Store) writeFile(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(s.dir, "state-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func (s *Store) remove(node vo.NodeRef) error {
	err := os.Remove(s.path(node))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) path(node vo.NodeRef) string {
	name := fmt.Sprintf("%s_%s.json", node.Participant, node.NodeID)
	return filepath.Join(s.dir, filepath.Base(name))
}

type runs struct {
	store *Store
}

func (r runs) Load(_ context.Context, node vo.NodeRef) (run.RunState, bool, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	file, found, err := r.store.read(node)
	if err != nil || !found {
		return run.RunState{}, false, err
	}
	state, err := toRunState(file)
	return state, err == nil, err
}

func (r runs) Update(_ context.Context, node vo.NodeRef, change func(*run.RunState)) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	file, _, err := r.store.read(node)
	if err != nil {
		return err
	}
	state, err := toRunState(file)
	if err != nil {
		return err
	}
	change(&state)
	return r.store.write(node, fromRunState(state, file.Mesh))
}

func (r runs) Forget(_ context.Context, node vo.NodeRef) error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()

	return r.store.remove(node)
}

type meshes struct {
	store *Store
}

func (m meshes) Identity(_ context.Context, shardID vo.ShardID, node vo.NodeRef) (mesh.Identity, bool, error) {
	state, found, err := m.state(shardID, node)
	if err != nil || !found || state.PublicKey == "" {
		return mesh.Identity{}, false, err
	}
	return toIdentity(node, *state), true, nil
}

func (m meshes) SaveIdentity(_ context.Context, shardID vo.ShardID, node vo.NodeRef, identity mesh.Identity) error {
	return m.update(shardID, node, func(state *meshState) {
		state.Address = identity.Member.Address
		state.PublicKey = identity.Member.PublicKey
		state.Signature = identity.Signature
	})
}

func (m meshes) Config(_ context.Context, shardID vo.ShardID, node vo.NodeRef) (mesh.Config, bool, error) {
	state, found, err := m.state(shardID, node)
	if err != nil || !found || len(state.Peers) == 0 {
		return mesh.Config{}, false, err
	}
	return toConfig(shardID, state.Peers), true, nil
}

func (m meshes) SaveConfig(_ context.Context, shardID vo.ShardID, node vo.NodeRef, config mesh.Config) error {
	return m.update(shardID, node, func(state *meshState) { state.Peers = fromConfig(config) })
}

func (m meshes) Forget(_ context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	return m.update(shardID, node, func(state *meshState) { *state = meshState{} })
}

func (m meshes) state(shardID vo.ShardID, node vo.NodeRef) (*meshState, bool, error) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()

	file, found, err := m.store.read(node)
	if err != nil || !found || file.Mesh == nil || vo.ShardID(file.ShardID) != shardID {
		return nil, false, err
	}
	return file.Mesh, true, nil
}

func (m meshes) update(shardID vo.ShardID, node vo.NodeRef, apply func(*meshState)) error {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()

	file, _, err := m.store.read(node)
	if err != nil {
		return err
	}
	if vo.ShardID(file.ShardID) != shardID {
		return nil
	}
	if file.Mesh == nil {
		file.Mesh = &meshState{}
	}
	apply(file.Mesh)
	return m.store.write(node, file)
}

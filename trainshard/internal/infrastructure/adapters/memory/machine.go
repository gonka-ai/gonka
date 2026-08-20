package memory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type Machine struct {
	mu         sync.Mutex
	log        *slog.Logger
	inventory  vo.GPUInventory
	images     map[vo.ImageDigest]struct{}
	containers map[vo.NodeRef]run.ContainerInfo
	volumes    map[vo.NodeRef]int64
	keys       map[vo.NodeRef]mesh.Member
	up         map[vo.NodeRef]struct{}
}

func New(log *slog.Logger, inventory vo.GPUInventory) *Machine {
	return &Machine{
		log:        log,
		inventory:  inventory,
		images:     map[vo.ImageDigest]struct{}{},
		containers: map[vo.NodeRef]run.ContainerInfo{},
		volumes:    map[vo.NodeRef]int64{},
		keys:       map[vo.NodeRef]mesh.Member{},
		up:         map[vo.NodeRef]struct{}{},
	}
}

func (m *Machine) Has(_ context.Context, digest vo.ImageDigest) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, present := m.images[digest]
	return present, nil
}

func (m *Machine) Pull(_ context.Context, digest vo.ImageDigest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.images[digest] = struct{}{}
	m.log.Info("pulled image", "image_digest", digest.Short())
	return nil
}

func (m *Machine) Layers(_ context.Context, digest vo.ImageDigest) (vo.ImageLayers, error) {
	return vo.ImageLayers{"sha256:" + strings.Repeat("0", 64)}, nil
}

func (m *Machine) Inspect(_ context.Context, _ vo.ShardID, node vo.NodeRef) (run.ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, found := m.containers[node]
	if !found {
		return run.ContainerInfo{State: vo.ContainerAbsent}, nil
	}
	return info, nil
}

func (m *Machine) Create(_ context.Context, spec run.ContainerSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if info, found := m.containers[spec.Node]; found && info.State != vo.ContainerAbsent {
		return fmt.Errorf("container for %s already exists", spec.Node)
	}
	m.containers[spec.Node] = run.ContainerInfo{State: vo.ContainerCreated, Image: spec.Run.Image, Revision: spec.Revision}
	m.log.Info("created container", "node_id", spec.Node.NodeID, "image_digest", spec.Run.Image.Short())
	return nil
}

func (m *Machine) Start(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	return m.transition(node, vo.ContainerRunning, "started container")
}

func (m *Machine) Stop(_ context.Context, _ vo.ShardID, node vo.NodeRef, _ time.Duration) error {
	return m.transition(node, vo.ContainerExited, "stopped container")
}

func (m *Machine) Remove(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.containers, node)
	m.log.Info("removed container", "node_id", node.NodeID)
	return nil
}

func (m *Machine) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) { return nil, nil }

func (m *Machine) transition(node vo.NodeRef, state vo.ContainerState, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, found := m.containers[node]
	if !found {
		return nil
	}
	exit := 0
	info.State = state
	if state == vo.ContainerExited {
		info.ExitCode = &exit
	}
	m.containers[node] = info
	m.log.Info(message, "node_id", node.NodeID)
	return nil
}

func (m *Machine) Logs(_ context.Context, req run.LogRequest, out io.Writer) error {
	_, err := fmt.Fprintf(out, "no logs: %s runs on an in-memory machine\n", req.Node)
	return err
}

func (m *Machine) Shell(context.Context, run.ExecRequest, io.ReadWriter) error {
	return shared.New("NO_SHELL", shared.ErrUnavailable, "an in-memory machine has no shell")
}

func (m *Machine) Allow(_ context.Context, _ vo.ShardID, _ vo.NodeRef, sources []vo.Source) ([]run.PinnedHost, error) {
	pinned := make([]run.PinnedHost, 0, len(sources))
	for _, source := range sources {
		pinned = append(pinned, run.PinnedHost{Name: source.Host, IP: "127.0.0.1"})
	}
	return pinned, nil
}

func (m *Machine) Ensure(_ context.Context, _ vo.ShardID, node vo.NodeRef, quotaBytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.volumes[node] = quotaBytes
	return nil
}

func (m *Machine) Usage(_ context.Context, _ vo.ShardID, node vo.NodeRef) (int64, int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	quota, present := m.volumes[node]
	return 0, quota, present, nil
}

func (m *Machine) Wipe(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.volumes, node)
	m.log.Info("wiped volumes", "node_id", node.NodeID)
	return nil
}

func (m *Machine) Archive(_ context.Context, shardID vo.ShardID, node vo.NodeRef, out io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, present := m.volumes[node]; !present {
		return run.ErrVolumeMissing
	}
	_, err := fmt.Fprintf(out, "no artifacts: shard %s on %s runs on an in-memory machine\n", shardID, node)
	return err
}

func (m *Machine) Inventory(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return m.inventory, nil
}

func (m *Machine) InUse(_ context.Context, node vo.NodeRef) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if info, found := m.containers[node]; found && info.State.Running() {
		return m.inventory.Count, nil
	}
	return 0, nil
}

func (m *Machine) ForeignWork(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return false, nil
}

func (m *Machine) TrainingProcesses(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return false, nil
}

func (m *Machine) KillTraining(context.Context, vo.ShardID, vo.NodeRef) error { return nil }

func (m *Machine) Mesh() mesh.Network { return network{machine: m} }

type network struct {
	machine *Machine
}

func (n network) Identity(_ context.Context, shardID vo.ShardID, node vo.NodeRef) (mesh.Member, error) {
	m := n.machine
	m.mu.Lock()
	defer m.mu.Unlock()

	if member, found := m.keys[node]; found {
		return member, nil
	}
	member := mesh.Member{
		Node:      node,
		Address:   fmt.Sprintf("10.%d.0.1", uint64(shardID)%256),
		PublicKey: fmt.Sprintf("memory-key-%s-%s", shardID, node.NodeID),
	}
	m.keys[node] = member
	m.log.Info("created mesh identity", "node_id", node.NodeID)
	return member, nil
}

func (n network) Apply(_ context.Context, _ vo.ShardID, node vo.NodeRef, peers []mesh.Peer) error {
	m := n.machine
	m.mu.Lock()
	defer m.mu.Unlock()

	m.up[node] = struct{}{}
	m.log.Info("applied mesh config", "node_id", node.NodeID, "peers", len(peers))
	return nil
}

func (n network) Present(_ context.Context, _ vo.ShardID, node vo.NodeRef) (bool, bool, error) {
	m := n.machine
	m.mu.Lock()
	defer m.mu.Unlock()

	_, key := m.keys[node]
	_, up := m.up[node]
	return key, up, nil
}

func (n network) Reach(_ context.Context, _ vo.ShardID, node vo.NodeRef, _ mesh.Peer) (bool, error) {
	m := n.machine
	m.mu.Lock()
	defer m.mu.Unlock()

	_, up := m.up[node]
	return up, nil
}

func (n network) Remove(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	m := n.machine
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.keys, node)
	delete(m.up, node)
	m.log.Info("removed mesh", "node_id", node.NodeID)
	return nil
}

func (n network) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) { return nil, nil }

func (m *Machine) GPUContainer(context.Context) error { return nil }

func (m *Machine) FreeDiskBytes(context.Context) (int64, error) { return 1 << 44, nil }

func (m *Machine) MeshPortReachable(context.Context) error { return nil }

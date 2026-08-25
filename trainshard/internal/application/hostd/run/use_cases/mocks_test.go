package usecases_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

var (
	now       = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	creator   = vo.Address("gonka1creator")
	stranger  = vo.Address("gonka1stranger")
	nodeA     = vo.NodeRef{Participant: "gonka1host", NodeID: "node-a"}
	nodeB     = vo.NodeRef{Participant: "gonka1host", NodeID: "node-b"}
	baseImage = vo.ImageDigest("base@sha256:" + strings.Repeat("a", 64))
	runImage  = vo.ImageDigest("run@sha256:" + strings.Repeat("b", 64))

	baseLayers = vo.ImageLayers{"layer-1"}
	runLayers  = vo.ImageLayers{"layer-1", "layer-2"}

	oldFault = shared.Fault{Code: "PULL_FAILED", Reason: "registry was down"}
)

const (
	shardID = vo.ShardID(7)
	height  = vo.Height(500)
	quota   = int64(1 << 30)
)

func activeShard() shard.Shard {
	return shard.Shard{
		ID:              shardID,
		Creator:         creator,
		Status:          shard.StatusActive,
		BaseImage:       baseImage,
		ExpiresAtHeight: 1000,
		Nodes:           []shard.ReservedNode{{Ref: nodeA, ModelID: "model-1"}},
	}
}

func runSpec() run.RunSpec {
	return run.RunSpec{
		Image:     runImage,
		Command:   []string{"train.py"},
		Sources:   []vo.Source{{Host: "s3.amazonaws.com", Port: 443}},
		Resources: run.Resources{GPUs: 8, DiskBytes: quota},
	}
}

func nodesCommand() usecases.NodesCommand {
	return usecases.NodesCommand{
		Shard:     shardID,
		Nodes:     []vo.NodeRef{nodeA},
		Actor:     shard.Actor{Address: creator},
		RequestID: "req-1",
		Deadline:  now.Add(time.Minute),
	}
}

func stopCommand() usecases.StopCommand {
	return usecases.StopCommand{NodesCommand: nodesCommand()}
}

type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recorder) sequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

type chainStub struct {
	height       vo.Height
	shards       map[vo.ShardID]shard.Shard
	reservations map[vo.NodeRef]vo.ShardID
	hardware     vo.GPUInventory
	releases     []vo.ReleaseReason
	err          error
}

func (c *chainStub) Height(context.Context) (vo.Height, error) { return c.height, c.err }

func (c *chainStub) Shard(_ context.Context, id vo.ShardID) (shard.Shard, bool, error) {
	if c.err != nil {
		return shard.Shard{}, false, c.err
	}
	record, found := c.shards[id]
	return record, found, nil
}

func (c *chainStub) Reservation(_ context.Context, node vo.NodeRef) (vo.ShardID, bool, error) {
	if c.err != nil {
		return 0, false, c.err
	}
	id, found := c.reservations[node]
	return id, found, nil
}

func (c *chainStub) Reserved(_ context.Context, node vo.NodeRef) (run.Reservation, bool, error) {
	if c.err != nil {
		return run.Reservation{}, false, c.err
	}
	id, found := c.reservations[node]
	if !found {
		return run.Reservation{}, false, nil
	}
	record, found := c.shards[id]
	if !found {
		return run.Reservation{}, false, nil
	}
	return run.Reservation{Shard: id, BaseImage: record.BaseImage, Active: record.IsActive(c.height)}, true, nil
}

func (c *chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return c.hardware, c.err
}

func (c *chainStub) OptIn(context.Context, vo.NodeRef, time.Duration) error { return c.err }

func (c *chainStub) Release(_ context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	if c.err != nil {
		return c.err
	}
	c.releases = append(c.releases, vo.ReleaseReason(fmt.Sprintf("%s:%s:%s", shardID, node.NodeID, reason)))
	delete(c.reservations, node)
	return nil
}

func (c *chainStub) ActiveShards(context.Context) ([]shard.Shard, error) {
	active := make([]shard.Shard, 0, len(c.shards))
	for _, record := range c.shards {
		active = append(active, record)
	}
	return active, c.err
}

type runStoreStub struct {
	rec     *recorder
	states  map[vo.NodeRef]run.RunState
	saveErr error
}

func (s *runStoreStub) Load(_ context.Context, node vo.NodeRef) (run.RunState, bool, error) {
	state, found := s.states[node]
	return state, found, nil
}

func (s *runStoreStub) Update(_ context.Context, node vo.NodeRef, change func(*run.RunState)) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.rec.record("runs.update")
	state := s.states[node]
	change(&state)
	s.states[node] = state
	return nil
}

func (s *runStoreStub) Forget(_ context.Context, node vo.NodeRef) error {
	s.rec.record("runs.forget")
	delete(s.states, node)
	return nil
}

type requestLogStub struct {
	results map[string][]run.NodeResult
}

func (l *requestLogStub) Result(_ context.Context, ref run.RequestRef) ([]run.NodeResult, bool, error) {
	results, found := l.results[ref.String()]
	return results, found, nil
}

func (l *requestLogStub) Record(_ context.Context, ref run.RequestRef, results []run.NodeResult) error {
	l.results[ref.String()] = results
	return nil
}

type imagesStub struct {
	rec     *recorder
	present map[vo.ImageDigest]bool
	layers  map[vo.ImageDigest]vo.ImageLayers
	pullErr error
}

func (i *imagesStub) Has(_ context.Context, digest vo.ImageDigest) (bool, error) {
	return i.present[digest], nil
}

func (i *imagesStub) Pull(_ context.Context, digest vo.ImageDigest) error {
	i.rec.record("images.pull")
	if i.pullErr != nil {
		return i.pullErr
	}
	i.present[digest] = true
	return nil
}

func (i *imagesStub) Layers(_ context.Context, digest vo.ImageDigest) (vo.ImageLayers, error) {
	return i.layers[digest], nil
}

type containersStub struct {
	rec        *recorder
	infos      map[vo.NodeRef]run.ContainerInfo
	created    run.ContainerSpec
	leftover   []vo.ShardID
	grace      time.Duration
	inspectErr error
	createErr  error
}

func (c *containersStub) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) {
	return c.leftover, nil
}

func (c *containersStub) Inspect(_ context.Context, _ vo.ShardID, node vo.NodeRef) (run.ContainerInfo, error) {
	if c.inspectErr != nil {
		return run.ContainerInfo{}, c.inspectErr
	}
	info, found := c.infos[node]
	if !found {
		return run.ContainerInfo{State: vo.ContainerAbsent}, nil
	}
	return info, nil
}

func (c *containersStub) Create(_ context.Context, spec run.ContainerSpec) error {
	c.rec.record("containers.create")
	if c.createErr != nil {
		return c.createErr
	}
	c.created = spec
	c.infos[spec.Node] = run.ContainerInfo{State: vo.ContainerCreated, Image: spec.Run.Image, Revision: spec.Revision}
	return nil
}

func (c *containersStub) Start(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	c.rec.record("containers.start")
	c.setState(node, vo.ContainerRunning)
	return nil
}

func (c *containersStub) Stop(_ context.Context, _ vo.ShardID, node vo.NodeRef, grace time.Duration) error {
	c.rec.record("containers.stop")
	c.grace = grace
	c.setState(node, vo.ContainerExited)
	return nil
}

func (c *containersStub) Remove(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	c.rec.record("containers.remove")
	c.infos[node] = run.ContainerInfo{State: vo.ContainerAbsent}
	return nil
}

func (c *containersStub) setState(node vo.NodeRef, state vo.ContainerState) {
	info := c.infos[node]
	info.State = state
	c.infos[node] = info
}

type volumesStub struct {
	rec     *recorder
	present map[vo.ShardID]bool
	used    int64
}

func (v *volumesStub) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) {
	held := make([]vo.ShardID, 0, len(v.present))
	for id, there := range v.present {
		if there {
			held = append(held, id)
		}
	}
	return held, nil
}

func (v *volumesStub) Ensure(_ context.Context, shardID vo.ShardID, _ vo.NodeRef, _ int64) error {
	v.rec.record("volumes.ensure")
	v.present[shardID] = true
	return nil
}

func (v *volumesStub) Usage(_ context.Context, shardID vo.ShardID, _ vo.NodeRef) (int64, int64, bool, error) {
	if !v.present[shardID] {
		return 0, 0, false, nil
	}
	return v.used, quota, true, nil
}

func (v *volumesStub) Wipe(_ context.Context, shardID vo.ShardID, _ vo.NodeRef) error {
	v.rec.record("volumes.wipe")
	delete(v.present, shardID)
	return nil
}

type egressStub struct {
	rec     *recorder
	sources []vo.Source
	err     error
}

func (e *egressStub) Allow(_ context.Context, _ vo.ShardID, _ vo.NodeRef, sources []vo.Source) ([]run.PinnedHost, error) {
	e.rec.record("egress.allow")
	if e.err != nil {
		return nil, e.err
	}
	e.sources = sources
	return nil, nil
}

type gpuStub struct {
	rec       *recorder
	foreign   bool
	inUse     int
	leftovers bool
}

func (g *gpuStub) Inventory(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{Model: "H100", Count: 8}, nil
}

func (g *gpuStub) InUse(context.Context, vo.NodeRef) (int, error) { return g.inUse, nil }

func (g *gpuStub) ForeignWork(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return g.foreign, nil
}

func (g *gpuStub) TrainingProcesses(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return g.leftovers, nil
}

func (g *gpuStub) KillTraining(context.Context, vo.ShardID, vo.NodeRef) error {
	g.rec.record("gpu.kill_training")
	g.leftovers = false
	return nil
}

type meshNetworkStub struct {
	rec  *recorder
	keys map[vo.ShardID]bool
	up   bool
}

func (m *meshNetworkStub) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) {
	held := make([]vo.ShardID, 0, len(m.keys))
	for id, there := range m.keys {
		if there {
			held = append(held, id)
		}
	}
	return held, nil
}

func (m *meshNetworkStub) Identity(_ context.Context, shardID vo.ShardID, node vo.NodeRef) (mesh.Member, error) {
	m.rec.record("mesh.identity")
	m.keys[shardID] = true
	return mesh.Member{Node: node, Address: "10.0.0.1", PublicKey: "public-key"}, nil
}

func (m *meshNetworkStub) Apply(context.Context, vo.ShardID, vo.NodeRef, []mesh.Peer) error {
	m.rec.record("mesh.apply")
	m.up = true
	return nil
}

func (m *meshNetworkStub) Present(_ context.Context, shardID vo.ShardID, _ vo.NodeRef) (bool, bool, error) {
	return m.keys[shardID], m.up, nil
}

func (m *meshNetworkStub) Reach(context.Context, vo.ShardID, vo.NodeRef, mesh.Peer) (bool, error) {
	return true, nil
}

func (m *meshNetworkStub) Interface(vo.NodeRef) (string, error) { return "ts0", nil }

func (m *meshNetworkStub) Remove(_ context.Context, shardID vo.ShardID, _ vo.NodeRef) error {
	m.rec.record("mesh.remove")
	delete(m.keys, shardID)
	m.up = false
	return nil
}

type meshStoreStub struct {
	rec        *recorder
	identities map[vo.NodeRef]mesh.Identity
	configs    map[vo.NodeRef]mesh.Config
}

func (s *meshStoreStub) Identity(_ context.Context, _ vo.ShardID, node vo.NodeRef) (mesh.Identity, bool, error) {
	identity, found := s.identities[node]
	return identity, found, nil
}

func (s *meshStoreStub) SaveIdentity(_ context.Context, _ vo.ShardID, node vo.NodeRef, identity mesh.Identity) error {
	s.rec.record("mesh_store.save_identity")
	s.identities[node] = identity
	return nil
}

func (s *meshStoreStub) Config(_ context.Context, _ vo.ShardID, node vo.NodeRef) (mesh.Config, bool, error) {
	config, found := s.configs[node]
	return config, found, nil
}

func (s *meshStoreStub) SaveConfig(_ context.Context, _ vo.ShardID, node vo.NodeRef, config mesh.Config) error {
	s.rec.record("mesh_store.save_config")
	s.configs[node] = config
	return nil
}

func (s *meshStoreStub) Forget(_ context.Context, _ vo.ShardID, node vo.NodeRef) error {
	s.rec.record("mesh_store.forget")
	delete(s.identities, node)
	delete(s.configs, node)
	return nil
}

type attestorStub struct {
	payloads [][]byte
}

func (a *attestorStub) Attest(_ context.Context, payload []byte) ([]byte, error) {
	a.payloads = append(a.payloads, payload)
	return []byte("signature"), nil
}

type controlStub struct {
	rec     *recorder
	drained bool
	stuck   bool
}

func (c *controlStub) Drained(context.Context, vo.NodeRef) (bool, error) { return c.drained, nil }

func (c *controlStub) Drain(context.Context, vo.NodeRef) (bool, error) {
	c.rec.record("control.drain")
	c.drained = !c.stuck
	return c.drained, nil
}

func (c *controlStub) Return(context.Context, vo.NodeRef) error {
	c.rec.record("control.return")
	c.drained = false
	return nil
}

type fixture struct {
	rec        *recorder
	converger  *run.Converger
	reconciler *usecases.ReconcileUseCase
	chain      *chainStub
	runs       *runStoreStub
	log        *requestLogStub
	images     *imagesStub
	containers *containersStub
	volumes    *volumesStub
	egress     *egressStub
	gpu        *gpuStub
	network    *meshNetworkStub
	store      *meshStoreStub
	attestor   *attestorStub
	control    *controlStub
	clock      *timex.Frozen
	limits     run.Limits
	patience   time.Duration
}

func newFixture() *fixture {
	rec := &recorder{}
	f := &fixture{
		rec: rec,
		chain: &chainStub{
			height:       height,
			shards:       map[vo.ShardID]shard.Shard{shardID: activeShard()},
			reservations: map[vo.NodeRef]vo.ShardID{nodeA: shardID},
		},
		runs:       &runStoreStub{rec: rec, states: map[vo.NodeRef]run.RunState{}},
		log:        &requestLogStub{results: map[string][]run.NodeResult{}},
		images:     &imagesStub{rec: rec, present: map[vo.ImageDigest]bool{}, layers: map[vo.ImageDigest]vo.ImageLayers{baseImage: baseLayers, runImage: runLayers}},
		containers: &containersStub{rec: rec, infos: map[vo.NodeRef]run.ContainerInfo{}},
		volumes:    &volumesStub{rec: rec, present: map[vo.ShardID]bool{}},
		egress:     &egressStub{rec: rec},
		gpu:        &gpuStub{rec: rec},
		network:    &meshNetworkStub{rec: rec, keys: map[vo.ShardID]bool{}},
		store:      &meshStoreStub{rec: rec, identities: map[vo.NodeRef]mesh.Identity{}, configs: map[vo.NodeRef]mesh.Config{}},
		attestor:   &attestorStub{},
		control:    &controlStub{rec: rec},
		clock:      timex.NewFrozen(now),
		limits:     run.Limits{MaxGPUs: 8, MaxDiskBytes: 1 << 40, MaxSources: 4},
		patience:   time.Hour,
	}
	f.converger = run.NewConverger(f.chain, f.runs, f.machine(), f.clock, f.patience)
	f.reconciler = usecases.NewReconcileUseCase(f.converger)
	return f
}

func (f *fixture) machine() run.Machine {
	return run.Machine{
		Images:     f.images,
		Containers: f.containers,
		Volumes:    f.volumes,
		GPU:        f.gpu,
		Mesh:       mesh.Runtime{Network: f.network, Store: f.store, Attestor: f.attestor},
		Egress:     f.egress,
		Control:    f.control,
		Runs:       f.runs,
		Clock:      f.clock,
		StopGrace:  time.Minute,
	}
}

func (f *fixture) reconcile() *usecases.ReconcileUseCase {
	return f.reconciler
}

func (f *fixture) deploy() *usecases.DeployUseCase {
	return usecases.NewDeployUseCase(f.chain, f.runs, f.log, f.containers, f.converger, f.clock, f.limits)
}

func (f *fixture) start() *usecases.StartUseCase {
	return usecases.NewStartUseCase(f.chain, f.runs, f.log, f.containers, f.converger, f.clock)
}

func (f *fixture) stop() *usecases.StopUseCase {
	return usecases.NewStopUseCase(f.chain, f.runs, f.log, f.containers, f.converger, f.clock)
}

func (f *fixture) status() *usecases.StatusUseCase {
	return usecases.NewStatusUseCase(f.chain, f.runs, f.machine(), f.clock)
}

func (f *fixture) abort() *usecases.AbortUseCase {
	return usecases.NewAbortUseCase(f.chain, f.chain)
}

func (f *fixture) report() *usecases.ReportUseCase {
	return usecases.NewReportUseCase(f.chain, f.runs, f.machine())
}

func (f *fixture) applyMesh() *usecases.ApplyMeshUseCase {
	return usecases.NewApplyMeshUseCase(f.chain, f.log, f.store, f.control, f.converger, f.clock)
}

func (f *fixture) prepared(ctx context.Context) error {
	for range 3 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			return err
		}
	}
	f.rec.reset()
	return nil
}

// meshed takes the node all the way a coordinator would before it deploys: prepared, and holding
// the peer list a container needs to be given its rank
func (f *fixture) meshed(ctx context.Context) error {
	if err := f.prepared(ctx); err != nil {
		return err
	}
	config, err := mesh.Order(shardID, []mesh.Member{{Node: nodeA, Address: "198.51.100.7:51820", PublicKey: "public-key"}})
	if err != nil {
		return err
	}
	if err := f.store.SaveConfig(ctx, shardID, nodeA, config); err != nil {
		return err
	}
	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		return err
	}
	f.rec.reset()
	return nil
}

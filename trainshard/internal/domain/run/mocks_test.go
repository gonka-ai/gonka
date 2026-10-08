package run_test

import (
	"context"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type recorder struct {
	calls []string
}

func (r *recorder) record(call string) { r.calls = append(r.calls, call) }

type reservationsStub struct {
	reservation run.Reservation
	found       bool
	releases    []vo.ReleaseReason
}

func (c *reservationsStub) Reserved(context.Context, vo.NodeRef) (run.Reservation, bool, error) {
	return c.reservation, c.found, nil
}

func (c *reservationsStub) Release(_ context.Context, _ vo.ShardID, _ vo.NodeRef, reason vo.ReleaseReason) error {
	c.releases = append(c.releases, reason)
	return nil
}

type runStoreStub struct {
	states         map[vo.NodeRef]run.RunState
	imageRecordErr error
}

func (s *runStoreStub) Load(_ context.Context, node vo.NodeRef) (run.RunState, bool, error) {
	state, found := s.states[node]
	return state, found, nil
}

func (s *runStoreStub) Update(_ context.Context, node vo.NodeRef, change func(*run.RunState)) error {
	state := s.states[node]
	images := len(state.Images)
	change(&state)
	if s.imageRecordErr != nil && len(state.Images) > images {
		return s.imageRecordErr
	}
	s.states[node] = state
	return nil
}

func (s *runStoreStub) Forget(_ context.Context, node vo.NodeRef) error {
	delete(s.states, node)
	return nil
}

type imagesStub struct {
	rec     *recorder
	present map[vo.ImageDigest]bool
}

func (i *imagesStub) Has(_ context.Context, digest vo.ImageDigest) (bool, error) {
	return i.present[digest], nil
}

func (i *imagesStub) Pull(_ context.Context, digest vo.ImageDigest) error {
	i.rec.record("images.pull")
	i.present[digest] = true
	return nil
}

func (i *imagesStub) Layers(context.Context, vo.ImageDigest) (vo.ImageLayers, error) {
	return runLayers, nil
}

type containersStub struct {
	rec    *recorder
	info   run.ContainerInfo
	shards []vo.ShardID
}

func (c *containersStub) Inspect(context.Context, vo.ShardID, vo.NodeRef) (run.ContainerInfo, error) {
	return c.info, nil
}

func (c *containersStub) Create(_ context.Context, spec run.ContainerSpec) error {
	c.rec.record("containers.create")
	c.info = run.ContainerInfo{State: vo.ContainerCreated, Image: spec.Run.Image, Revision: spec.Revision}
	return nil
}

func (c *containersStub) Start(context.Context, vo.ShardID, vo.NodeRef) error {
	c.rec.record("containers.start")
	c.info.State = vo.ContainerRunning
	return nil
}

func (c *containersStub) Stop(context.Context, vo.ShardID, vo.NodeRef, time.Duration) error {
	c.rec.record("containers.stop")
	c.info.State = vo.ContainerExited
	return nil
}

func (c *containersStub) Remove(context.Context, vo.ShardID, vo.NodeRef) error {
	c.rec.record("containers.remove")
	c.info = run.ContainerInfo{State: vo.ContainerAbsent}
	return nil
}

func (c *containersStub) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) {
	return c.shards, nil
}

type volumesStub struct {
	rec     *recorder
	shardID vo.ShardID
	present bool
}

func (v *volumesStub) Ensure(context.Context, vo.ShardID, vo.NodeRef, int64) error {
	v.rec.record("volumes.ensure")
	v.present = true
	return nil
}

func (v *volumesStub) Usage(context.Context, vo.ShardID, vo.NodeRef) (int64, int64, bool, error) {
	return 0, 0, v.present, nil
}

func (v *volumesStub) Wipe(context.Context, vo.ShardID, vo.NodeRef) error {
	v.rec.record("volumes.wipe")
	v.present = false
	return nil
}

func (v *volumesStub) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) {
	if !v.present {
		return nil, nil
	}
	return []vo.ShardID{v.shardID}, nil
}

type egressStub struct{}

func (egressStub) Allow(context.Context, vo.ShardID, vo.NodeRef, []vo.Source) ([]run.PinnedHost, error) {
	return nil, nil
}

func (egressStub) Fenced(context.Context, vo.ShardID, vo.NodeRef) (bool, error) { return true, nil }

type gpuStub struct {
	rec *recorder
	err error
}

func (g *gpuStub) Inventory(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{Profile: "H100", Count: 8}, g.err
}

func (g *gpuStub) InUse(context.Context, vo.NodeRef) (int, error) { return 0, g.err }

func (g *gpuStub) ForeignWork(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return false, g.err
}

func (g *gpuStub) TrainingProcesses(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return false, nil
}

func (g *gpuStub) KillTraining(context.Context, vo.ShardID, vo.NodeRef) error {
	g.rec.record("gpu.kill_training")
	return nil
}

type networkStub struct {
	rec     *recorder
	shardID vo.ShardID
	key     bool
	up      bool
}

func (n *networkStub) Create(context.Context, vo.ShardID, vo.NodeRef) error {
	n.rec.record("mesh.create")
	n.key = true
	return nil
}

func (n *networkStub) Identified(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return n.key, nil
}

func (n *networkStub) Configured(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return n.key, nil
}

func (n *networkStub) Present(context.Context, vo.ShardID, vo.NodeRef) (bool, bool, error) {
	return n.key, n.up, nil
}

func (n *networkStub) Placement(context.Context, vo.ShardID, vo.NodeRef) (vo.Placement, error) {
	return vo.Placement{Size: 1, Master: "10.7.0.1"}, nil
}

func (n *networkStub) Apply(context.Context, vo.ShardID, vo.NodeRef) error {
	n.rec.record("mesh.apply")
	n.up = true
	return nil
}

func (n *networkStub) Remove(context.Context, vo.ShardID, vo.NodeRef) error {
	n.rec.record("mesh.remove")
	n.key, n.up = false, false
	return nil
}

func (n *networkStub) Shards(context.Context, vo.NodeRef) ([]vo.ShardID, error) {
	if !n.key {
		return nil, nil
	}
	return []vo.ShardID{n.shardID}, nil
}

type controlStub struct {
	rec        *recorder
	unreadable error
}

func (c *controlStub) Drained(context.Context, vo.NodeRef) (bool, error) {
	return c.unreadable == nil, c.unreadable
}

func (c *controlStub) Drain(context.Context, vo.NodeRef) (bool, error) {
	c.rec.record("control.drain")
	return true, nil
}

func (c *controlStub) Return(context.Context, vo.NodeRef) error {
	c.rec.record("control.return")
	return nil
}

type clockStub struct {
	now time.Time
}

func (c clockStub) Now() time.Time { return c.now }

package usecases_test

import (
	"context"
	"errors"
	"time"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

var (
	nodeA    = vo.NodeRef{Participant: "gonka1host", NodeID: "node-a"}
	hardware = vo.GPUInventory{Model: "H100", Count: 8}
	errProbe = errors.New("no nvidia runtime")
)

const (
	version   = "v0.1.0"
	freeDisk  = int64(2 << 40)
	diskFloor = int64(1 << 40)
)

type probeStub struct {
	gpuContainer error
	freeDisk     int64
	freeDiskErr  error
	meshPort     error
}

func newProbeStub() *probeStub { return &probeStub{freeDisk: freeDisk} }

func (p *probeStub) GPUContainer(context.Context) error { return p.gpuContainer }

func (p *probeStub) FreeDiskBytes(context.Context) (int64, error) {
	return p.freeDisk, p.freeDiskErr
}

func (p *probeStub) MeshPortReachable(context.Context) error { return p.meshPort }

type gpuStub struct {
	inventory vo.GPUInventory
	err       error
}

func (g *gpuStub) Inventory(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return g.inventory, g.err
}

func (g *gpuStub) InUse(context.Context, vo.NodeRef) (int, error) { return 0, nil }

func (g *gpuStub) ForeignWork(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return false, nil
}

func (g *gpuStub) TrainingProcesses(context.Context, vo.ShardID, vo.NodeRef) (bool, error) {
	return false, nil
}

func (g *gpuStub) KillTraining(context.Context, vo.ShardID, vo.NodeRef) error { return nil }

type chainStub struct {
	hardware vo.GPUInventory
	err      error
}

func (c *chainStub) Height(context.Context) (vo.Height, error) { return 500, nil }

func (c *chainStub) Shard(context.Context, vo.ShardID) (shard.Shard, bool, error) {
	return shard.Shard{}, false, nil
}

func (c *chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return 0, false, nil
}

func (c *chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

func (c *chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return c.hardware, c.err
}

type submitterStub struct {
	ttls []time.Duration
	err  error
}

func (s *submitterStub) OptIn(_ context.Context, _ vo.NodeRef, ttl time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.ttls = append(s.ttls, ttl)
	return nil
}

func (s *submitterStub) Release(context.Context, vo.ShardID, vo.NodeRef, vo.ReleaseReason) error {
	return nil
}

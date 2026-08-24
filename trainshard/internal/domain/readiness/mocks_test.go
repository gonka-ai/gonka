package readiness_test

import (
	"context"
	"errors"
	"time"

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

type clockStub struct{ now time.Time }

func newClockStub() *clockStub { return &clockStub{now: time.Unix(1700000000, 0).UTC()} }

func (c *clockStub) Now() time.Time { return c.now }

type probeStub struct {
	gpuContainer error
	gpuAsked     int
	freeDisk     int64
	freeDiskErr  error
	meshPort     error
}

func newProbeStub() *probeStub { return &probeStub{freeDisk: freeDisk} }

func (p *probeStub) GPUContainer(context.Context) error {
	p.gpuAsked++
	return p.gpuContainer
}

func (p *probeStub) FreeDiskBytes(context.Context) (int64, error) {
	return p.freeDisk, p.freeDiskErr
}

func (p *probeStub) MeshPortReachable(context.Context) error { return p.meshPort }

type cardsStub struct {
	inventory vo.GPUInventory
	err       error
}

func (c *cardsStub) Inventory(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return c.inventory, c.err
}

type claimStub struct {
	hardware vo.GPUInventory
	err      error
}

func (c *claimStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return c.hardware, c.err
}

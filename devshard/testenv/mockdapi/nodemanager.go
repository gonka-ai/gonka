package mockdapi

import (
	"context"
	"fmt"
	"sync/atomic"

	"google.golang.org/grpc"

	nmpb "devshard/mlnode/gen"
)

// NoopNodeManager satisfies the generated gen.NodeManagerClient contract
// with deterministic, non-dereferencing stubs.
//
// Stub engines in the testenv (see devshard/testenv/engine) never touch
// the endpoint or node id — this implementation only exists so the
// contract a production devshardd expects ("a NodeManagerClient is
// injected") is satisfied byte-for-byte without introducing a second
// wiring shape. `AcquireMLNode` hands out a monotonically-increasing
// synthetic lock id so tests that assert "unique per call" pass; a
// fixed placeholder endpoint and node id are returned so scenarios that
// want heterogeneous per-node behavior can still distinguish the one
// node by id (extended in Phase 7b/7c per testenv-stub-engines.md).
//
// ReleaseMLNode records nothing and returns success regardless of the
// outcome code. This matches the "never validates, never penalizes"
// posture of the testenv node manager.
type NoopNodeManager struct {
	lockCounter atomic.Uint64
	acquires    atomic.Uint64
	releases    atomic.Uint64
}

// NewNoopNodeManager constructs the stub. Safe for concurrent use.
func NewNoopNodeManager() *NoopNodeManager { return &NoopNodeManager{} }

// AcquireMLNode returns a synthetic lock + fixed endpoint + fixed node
// id. Never returns ResourceExhausted — the stub has "infinite"
// capacity, which matches the testenv's intent (no capacity faults
// unless explicitly injected by a future stub-engines control plane).
func (n *NoopNodeManager) AcquireMLNode(
	ctx context.Context,
	in *nmpb.AcquireMLNodeRequest,
	opts ...grpc.CallOption,
) (*nmpb.AcquireMLNodeResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seq := n.lockCounter.Add(1)
	n.acquires.Add(1)
	return &nmpb.AcquireMLNodeResponse{
		LockId:   fmt.Sprintf("mockdapi-lock-%d", seq),
		Endpoint: "mockdapi://noop",
		NodeId:   "mockdapi-node-0",
	}, nil
}

// ReleaseMLNode is a no-op; it returns the empty response unconditionally.
func (n *NoopNodeManager) ReleaseMLNode(
	ctx context.Context,
	in *nmpb.ReleaseMLNodeRequest,
	opts ...grpc.CallOption,
) (*nmpb.ReleaseMLNodeResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n.releases.Add(1)
	return &nmpb.ReleaseMLNodeResponse{}, nil
}

// AcquireCount returns the number of successful AcquireMLNode calls.
// Exposed for test assertions on wiring behavior (e.g. retry loops).
func (n *NoopNodeManager) AcquireCount() uint64 { return n.acquires.Load() }

// ReleaseCount returns the number of ReleaseMLNode calls. Exposed for
// tests that assert release-on-success paths.
func (n *NoopNodeManager) ReleaseCount() uint64 { return n.releases.Load() }

// Compile-time assertion that NoopNodeManager implements the full
// gen.NodeManagerClient surface. If the protobuf definition grows new
// RPCs this line fails the build instead of silently drifting.
var _ nmpb.NodeManagerClient = (*NoopNodeManager)(nil)

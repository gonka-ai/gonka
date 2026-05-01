package mockdapi

import (
	"context"
	"fmt"

	"devshard/blockoracle"
	"devshard/blockoracle/client"
	nmpb "devshard/mlnode/gen"
)

// MockDapi is the in-process library that devshardd-testenv links in
// place of a real decentralized-api client. It packages the two
// dapi-facing interfaces devshardd depends on — a BlockOracle and a
// NodeManagerClient — behind a single handle whose lifetime is bounded
// by a Close call (which tears down the oracle's background SSE
// subscription).
//
// Fields are exported so wiring code can pick them up directly:
//
//	md, err := mockdapi.New(ctx, mockdapi.Config{...})
//	h, _ := host.NewHost(..., host.WithBlockOracle(md.Oracle))
//	// inject md.NodeManager wherever a prod NodeManagerClient goes.
//
// See devshard/docs/testenv.md §5 for the wiring snippet.
type MockDapi struct {
	// Oracle serves blockoracle.BlockOracle. In tests it is a
	// trust-mode client.Client; callers that need At / Prove can type-
	// assert but the stable surface is the interface.
	Oracle blockoracle.BlockOracle

	// NodeManager satisfies the generated gen.NodeManagerClient
	// interface. It is a NoopNodeManager in tree today; scenarios that
	// need heterogeneous per-node behavior swap in a richer
	// implementation at the call site.
	NodeManager nmpb.NodeManagerClient

	// oracleClient retains the concrete client so Close can tear down
	// its subscribe goroutine. Nil if the oracle was supplied via a
	// test hook.
	oracleClient *client.Client
}

// New constructs a MockDapi. Callers are expected to hand the returned
// handle's Oracle / NodeManager fields to host.NewHost (or equivalent)
// and call Close when shutting down. The supplied ctx is the parent of
// the oracle's background subscribe goroutine; cancelling ctx is
// equivalent to calling Close.
func New(ctx context.Context, cfg Config) (*MockDapi, error) {
	if ctx == nil {
		return nil, fmt.Errorf("mockdapi: nil context")
	}
	oc, err := newOracleClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("mockdapi: build oracle client: %w", err)
	}
	return &MockDapi{
		Oracle:       oc,
		NodeManager:  NewNoopNodeManager(),
		oracleClient: oc,
	}, nil
}

// Close stops the oracle's background subscription. Safe to call more
// than once; subsequent calls are no-ops. Does not close the
// NodeManager (the stub has no resources to release).
//
// Close is deliberately idempotent so scenarios that wire a deferred
// Close in tests and also tear down via context cancellation do not
// panic.
func (m *MockDapi) Close() {
	if m == nil {
		return
	}
	if m.oracleClient != nil {
		m.oracleClient.Close()
		m.oracleClient = nil
	}
}

// Package bridge implements devshard/bridge.MainnetBridge on top of the
// testenv mock-chain gRPC service.
//
// It is imported only by cmd/devshardd-testenv; production devshardd must
// not depend on this package. The bridge lives in devshard/testenv/bridge
// rather than devshard/bridge to keep that dependency rule obvious.
package bridge

// TODO(phase-4): GRPCBridge struct and NewGRPCBridge(ctx, mockChainURL).
// Implement all methods of devshard/bridge.MainnetBridge by round-tripping
// to mock-chain. See devshard/docs/testenv.md §Phase 4.

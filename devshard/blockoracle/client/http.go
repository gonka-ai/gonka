// Package client consumes the blockoracle HTTP + SSE API over the network
// or in-process.
//
// It subscribes on startup, caches the latest header, re-verifies every
// ingested header against a pinned validator set, and serves the
// BlockOracle interface to downstream callers (devshardd, internal dapi
// callers).
package client

// TODO(phase-1): NewHTTP(baseURL, verifier, opts...) blockoracle.BlockOracle
// TODO(phase-1): NewInProcess(oracle blockoracle.BlockOracle) for dapi reuse.
// See devshard/docs/testenv.md §3.5.

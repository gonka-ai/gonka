// Package blockoracle provides an authenticated view of mainnet block
// headers (height, block hash, app hash, commit signatures) to devshard
// hosts and other consumers.
//
// It is the single producer-consumer contract shared by two runtimes:
//
//   - In production, decentralized-api links blockoracle in-process,
//     instantiates a ChainObserver backed by Tendermint RPC, and serves
//     the HTTP + SSE API from its own router.
//   - In the testenv, the height-sync container instantiates a mock
//     ChainObserver that fabricates signed headers and serves the same
//     HTTP + SSE API. devshardd-testenv hosts subscribe to it via the
//     client sub-package.
//
// Consumers call the package through the BlockOracle interface; the HTTP
// client variant subscribes to the SSE stream, caches headers, and
// re-verifies every header against a pinned validator set before ingest.
//
// Strict dependency rule: this package and all its sub-packages MUST NOT
// import anything under devshard/testenv. See devshard/docs/testenv.md §3
// for the full design.
package blockoracle

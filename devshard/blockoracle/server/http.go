// Package server mounts the blockoracle HTTP + SSE API on a host router.
//
// The same Mount() is called in:
//
//   - the standalone height-sync binary (testenv)
//   - real decentralized-api (production)
//
// so devshardd consumers see an identical wire protocol in both
// environments.
package server

// TODO(phase-1): Mount(group, oracle) registers
//   GET  /block/latest
//   GET  /block/:height
//   GET  /block/prove?path=&height=
//   GET  /block/stream   (SSE)
// See devshard/docs/testenv.md §3.4.

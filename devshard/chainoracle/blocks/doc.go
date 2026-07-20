// Package blocks provides authenticated mainnet block headers (height, block
// hash, app hash, commit signatures) to devshard hosts and other consumers.
//
// Ported from devshard-testenv blockoracle and renamed as part of the unified
// chainoracle module (see devshard/docs/testenv-v2-plan.md Phase 2).
//
// Production decentralized-api will mount the HTTP + SSE API in-process with a
// real Tendermint observer; testenv mock-dapi mounts the same routes via
// devshard/chainoracle/server with a mock observer.
//
// Strict dependency rule: this package and sub-packages MUST NOT import
// devshard/testenv, devshard/host, or devshard/heightsync.
package blocks

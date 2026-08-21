// Package blocks provides authenticated mainnet block headers (height, block
// hash, app hash, commit signatures) to devshard hosts and other consumers.
//
// Ported from devshard-testenv blockoracle and renamed as part of the unified
// chainoracle module (see devshard/docs/testenv-v2-plan.md Phase 2).
//
// Hosts take Latest/Subscribe from the existing Comet NewBlock subscription
// (hash + time on the event). HTTP lookup is GET /block/:height and
// GET /block/:height/prove. Missing /block/:height (0.2.15 dapi)
// returns a dummy header so L6 does not mark.
//
// Production decentralized-api and testenv mock-dapi mount those unary
// routes via devshard/chainoracle/server.
//
// Strict dependency rule: this package and sub-packages MUST NOT import
// devshard/testenv, devshard/host, or devshard/heightsync.
package blocks

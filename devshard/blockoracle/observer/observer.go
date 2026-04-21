// Package observer produces authenticated block headers from a chain
// source (Tendermint RPC in production, a mock fabricator in testenv).
//
// It is split from the root blockoracle package so that callers that only
// need the BlockOracle interface and the Header types do not pull in
// Tendermint client dependencies.
package observer

// TODO(phase-1): define the Observer interface and NewTendermint
// constructor. See devshard/docs/testenv.md §3.5.

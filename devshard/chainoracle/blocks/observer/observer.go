// Package observer produces authenticated block headers from a chain
// source (Tendermint RPC in production, a mock fabricator in testenv).
//
// It is split from the root blockoracle package so callers that only
// need the BlockOracle interface and the Header types do not pull in
// Tendermint client dependencies.
package observer

import (
	"context"

	"devshard/chainoracle/blocks"
)

// Observer is a producer-side BlockOracle that runs a background loop to
// discover new headers. Run blocks until ctx is cancelled or the underlying
// source errs fatally; callers typically run it in a goroutine and
// terminate by cancelling the context.
type Observer interface {
	blocks.BlockOracle
	Run(ctx context.Context) error
}

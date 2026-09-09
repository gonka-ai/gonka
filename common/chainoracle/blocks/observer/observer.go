// Package observer produces hash-only block headers from a CometBFT source.
//
// Production dapi mounts NewTendermint as GET /block/:height. Hosts and
// the gateway reuse HeaderFromNewBlock on the existing Comet subscription.
// The testenv mock fabricator stays in the devshard module so signing is
// not compiled into the producer path.
package observer

import (
	"context"

	"common/chainoracle/blocks"
)

// Observer is a producer-side BlockOracle that runs a background loop to
// discover new headers. Run blocks until ctx is cancelled or the underlying
// source errs fatally; callers typically run it in a goroutine and
// terminate by cancelling the context.
type Observer interface {
	blocks.BlockOracle
	Run(ctx context.Context) error
}

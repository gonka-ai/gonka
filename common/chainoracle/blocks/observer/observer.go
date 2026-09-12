// Package observer produces hash-only block headers from a CometBFT source.
//
// Production dapi mounts NewTendermint as GET /block/:height. Tip is the
// existing NewBlock listener (Observe), not /block/latest or /block/stream.
// At() uses tipcache then Comet Header RPC. The testenv mock fabricator
// stays in the devshard module so signing is not compiled into the producer
// path.
package observer

import (
	"common/chainoracle/blocks"
)

// Observer is a producer-side BlockOracle. Tip is pushed via Observe
// (Comet NewBlock); At falls back to Header RPC.
type Observer interface {
	blocks.BlockOracle
	Observe(h *blocks.Header)
}

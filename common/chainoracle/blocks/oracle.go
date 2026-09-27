package blocks

import (
	"context"
	"errors"
	"time"
)

// ErrProveNotImplemented is returned by hash-only producers (height + hash,
// no LightBlock). HTTP mounts map it to 501; Anchor and heartbeat must not
// depend on Prove.
var ErrProveNotImplemented = errors.New("blockoracle: prove not implemented")

// ErrHeaderNotFound is a missing height (pruned, unknown, or empty Comet
// response), or a dapi with no oracle configured.
var ErrHeaderNotFound = errors.New("blockoracle: header not found")

// ErrHeaderRPCUnimplemented is a dapi (or stub) that has no GetBlockHeader
// route. Failover skips Latest() and At() against that backend until the
// next GetBlockHeader re-probe (15m).
var ErrHeaderRPCUnimplemented = errors.New("blockoracle: header rpc unimplemented")

// HistoryWindow is how far below the tip At() retains.
// oldest = max(1, tip − HistoryWindow).
const HistoryWindow = 100

// OldestHeight is the inclusive floor of the retained window for tip.
func OldestHeight(tip int64) int64 {
	oldest := tip - HistoryWindow
	if oldest < 1 {
		return 1
	}
	return oldest
}

// BlockOracle is the stable contract between producers (observers, the
// standalone binary, the in-process dapi mount) and consumers (devshardd
// hosts, real dapi internals).
//
// All implementations MUST return pre-verified headers; consumers that
// ingest a header are expected to re-verify locally as defence-in-depth,
// but they are not required to re-prove commit signatures on every access.
type BlockOracle interface {
	Latest(ctx context.Context) (*Header, error)
	At(ctx context.Context, height int64) (*Header, error)
	Prove(ctx context.Context, path string, height int64) (*Proof, error)
	Subscribe(ctx context.Context, fromHeight int64) (<-chan *Header, error)
}

// HashOnlyHeader is the hash-only wire payload: height, block hash,
// block timestamp, and chain id. Commit and validator hashes stay empty
// until Strong (spec §8 / §15).
func HashOnlyHeader(height int64, t time.Time, chainID string, blockHash []byte) *Header {
	return &Header{
		Height:    height,
		Time:      t,
		ChainID:   chainID,
		BlockHash: append([]byte(nil), blockHash...),
	}
}

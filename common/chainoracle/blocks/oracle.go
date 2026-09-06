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
// meta). HTTP mounts map it to 404. Transport and other internal failures
// must not use this sentinel — those stay 5xx so host failover can tell
// "old dapi / no route" from "this dapi is up, RPC failed" (§7.3).
var ErrHeaderNotFound = errors.New("blockoracle: header not found")

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

package headertime

import (
	"context"
	"fmt"
	"time"

	"common/chainoracle/blocks"
)

// Filler answers a miss for one height. Source stores a real header and
// ignores a dummy, a zero time, or an empty hash.
type Filler interface {
	Fill(ctx context.Context, height int64) (Fill, error)
}

// BatchFiller answers a catch-up span in one read. Source stores the answers
// together. from and to are inclusive.
type BatchFiller interface {
	FillBatch(ctx context.Context, from, to int64) ([]Fill, error)
}

// Fill is one filler answer.
type Fill struct {
	Header   *blocks.Header
	Origin   Origin
	Verified bool
}

type HistoryOracle interface {
	HeaderAt(ctx context.Context, height int64) (*blocks.Header, error)
}

type WaitingOracle interface {
	HeaderAtWait(ctx context.Context, height int64, wait time.Duration) (*blocks.Header, error)
}

type oracleFiller struct {
	oracle   blocks.BlockOracle
	origin   Origin
	verified bool
}

// dapiOrigin is an oracle that remembers which heights came from dapi BLOCKs.
type dapiOrigin interface {
	FromDAPI(height int64) bool
}

// OracleFiller adapts a block oracle to the miss path. HeaderAtWait, then
// HeaderAt, then At. The poller replaces this with header-only reads.
func OracleFiller(oracle blocks.BlockOracle, origin Origin, verified bool) Filler {
	return oracleFiller{oracle: oracle, origin: origin, verified: verified}
}

func (f oracleFiller) Fill(ctx context.Context, height int64) (Fill, error) {
	h, err := f.fetch(ctx, height)
	if err != nil {
		return Fill{}, err
	}
	return Fill{Header: h, Origin: f.originOf(h), Verified: f.verified}, nil
}

// FillBatch reads from..to without the caller's wait budget. A direct lookup
// keeps HeaderAtWait. Catch-up stores the slice in one apply.
func (f oracleFiller) FillBatch(ctx context.Context, from, to int64) ([]Fill, error) {
	if from <= 0 || to < from {
		return nil, nil
	}
	// Drop the caller's deadline so this loop does not turn into HeaderAtWait,
	// then bound the batch so a hung RPC cannot stall every later catch-up.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultBatchTimeout)
	defer cancel()
	var out []Fill
	for h := from; h <= to; h++ {
		if err := ctx.Err(); err != nil {
			if len(out) == 0 {
				return nil, err
			}
			return out, err
		}
		got, err := f.fetch(ctx, h)
		if err != nil || got == nil {
			continue
		}
		out = append(out, Fill{Header: got, Origin: f.originOf(got), Verified: f.verified})
	}
	if err := ctx.Err(); err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

func (f oracleFiller) originOf(h *blocks.Header) Origin {
	if h != nil {
		if d, ok := f.oracle.(dapiOrigin); ok && d.FromDAPI(h.Height) {
			return OriginDAPI
		}
	}
	return f.origin
}

func (f oracleFiller) fetch(ctx context.Context, height int64) (*blocks.Header, error) {
	if f.oracle == nil {
		return nil, fmt.Errorf("no block oracle")
	}
	wait := time.Duration(0)
	if dl, ok := ctx.Deadline(); ok {
		wait = time.Until(dl) - time.Second
	}
	if w, ok := f.oracle.(WaitingOracle); ok && wait > 0 {
		return w.HeaderAtWait(ctx, height, wait)
	}
	if w, ok := f.oracle.(blocks.HeaderWaiter); ok && wait > 0 {
		return w.AtWait(ctx, height, wait)
	}
	if hist, ok := f.oracle.(HistoryOracle); ok {
		return hist.HeaderAt(ctx, height)
	}
	return f.oracle.At(ctx, height)
}

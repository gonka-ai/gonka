package transport

import (
	"context"
	"sync/atomic"

	"devshard/chainoracle/blocks"
)

type heightSyncTestOracle struct {
	hdr         *blocks.Header
	latestCalls atomic.Int64
}

func (o *heightSyncTestOracle) LatestCalls() int64 {
	if o == nil {
		return 0
	}
	return o.latestCalls.Load()
}

func (o *heightSyncTestOracle) Latest(context.Context) (*blocks.Header, error) {
	o.latestCalls.Add(1)
	if o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *heightSyncTestOracle) At(context.Context, int64) (*blocks.Header, error) { return nil, nil }

func (o *heightSyncTestOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, nil
}

func (o *heightSyncTestOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

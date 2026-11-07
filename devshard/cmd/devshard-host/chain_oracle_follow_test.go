package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"devshard/chainoracle/blocks/headertime"

	"github.com/stretchr/testify/require"
)

type scriptOracle struct {
	mu    sync.Mutex
	tip   int64
	times map[int64]time.Time
	at    map[int64]int
}

func (o *scriptOracle) Latest(context.Context) (*blocks.Header, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.headerLocked(o.tip)
}

func (o *scriptOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.at[height]++
	return o.headerLocked(height)
}

func (o *scriptOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *scriptOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	return nil, fmt.Errorf("unused")
}

func (o *scriptOracle) headerLocked(height int64) (*blocks.Header, error) {
	if height <= 0 {
		return nil, fmt.Errorf("no header")
	}
	t, ok := o.times[height]
	if !ok {
		return nil, fmt.Errorf("missing height %d", height)
	}
	return blocks.HashOnlyHeader(height, t, "test", []byte{byte(height)}), nil
}

func (o *scriptOracle) setTip(height int64, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tip = height
	o.times[height] = at
}

func TestObserveChainProgressBackfillsGaps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oracle := &scriptOracle{
		tip:   150,
		times: map[int64]time.Time{150: now},
		at:    map[int64]int{},
	}
	src := headertime.New()
	src.Observe(blocks.HashOnlyHeader(150, now, "test", []byte{150}), headertime.OriginChain, false)

	oracle.setTip(152, now.Add(2*time.Second))
	oracle.times[151] = now.Add(time.Second)

	got := observeChainProgress(context.Background(), oracle, src, 150)
	require.Equal(t, int64(152), got)
	require.Equal(t, 1, oracle.at[151], "gap height must be fetched")

	for _, height := range []uint64{150, 151, 152} {
		rec, ok := src.StoredAt(height)
		require.True(t, ok, "height %d", height)
		require.Equal(t, []byte{byte(height)}, rec.Hash)
	}
}

func TestObserveChainProgressNoopWhenTipUnchanged(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	oracle := &scriptOracle{
		tip:   150,
		times: map[int64]time.Time{150: now},
		at:    map[int64]int{},
	}
	src := headertime.New()
	got := observeChainProgress(context.Background(), oracle, src, 150)
	require.Equal(t, int64(150), got)
	require.Empty(t, oracle.at)
}

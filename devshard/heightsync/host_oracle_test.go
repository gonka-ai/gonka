package heightsync_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/failover"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

type recChain struct {
	hdr   *blocks.Header
	calls atomic.Int64
}

func (r *recChain) Latest(context.Context) (*blocks.Header, error) {
	r.calls.Add(1)
	cp := *r.hdr
	cp.BlockHash = append([]byte(nil), r.hdr.BlockHash...)
	return &cp, nil
}
func (r *recChain) At(ctx context.Context, _ int64) (*blocks.Header, error) { return r.Latest(ctx) }
func (r *recChain) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}
func (r *recChain) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func TestHostOracle_DecideUsesCometTip(t *testing.T) {
	tip := &recChain{hdr: blocks.HashOnlyHeader(99, time.Unix(2, 0).UTC(), "gonka-test", []byte{9, 9})}
	o := failover.New(tip, nil, nil)
	o.SetCometConnected(true)
	sec, err, miss := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, o).Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, int64(99), sec.MainnetHeight)
}

func TestHostOracle_DecideSnapshotDoesNotCallNMLatest(t *testing.T) {
	nm := &recChain{hdr: blocks.HashOnlyHeader(7, time.Unix(1, 0).UTC(), "gonka-test", []byte{1, 2, 3, 4})}
	o := failover.New(nil, nm, nil)
	sec, err, miss := heightsync.MustNewAnchorSchedulerFromOracle(10, 1, o).Decide(context.Background(), heightsync.DecideHints{Nonce: 1})
	require.NoError(t, err)
	require.False(t, miss)
	require.Equal(t, int64(7), sec.MainnetHeight)
	require.Equal(t, int64(1), nm.calls.Load(), "debug snapshot must not add a GetBlockHeader Latest on top of the section fetch")
}

package heightsync_test

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

type evalOracle struct {
	hdr         *blocks.Header
	stale       bool
	err         error
	proveCalled bool
	latestCalls int
}

func (o *evalOracle) Latest(context.Context) (*blocks.Header, error) {
	o.latestCalls++
	if o.err != nil {
		return nil, o.err
	}
	if o.hdr == nil {
		return nil, context.Canceled
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *evalOracle) At(ctx context.Context, _ int64) (*blocks.Header, error) {
	return o.Latest(ctx)
}

func (o *evalOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	o.proveCalled = true
	return nil, blocks.ErrProveNotImplemented
}

func (o *evalOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}
func (o *evalOracle) Stale() bool { return o.stale }

func TestEvaluateSyncState_Table(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	ctx := context.Background()
	hashOnly := blocks.HashOnlyHeader(500, time.Unix(1, 0).UTC(), "gonka-test", []byte{1, 2, 3})

	synced := heightsync.EvaluateSyncState(ctx, &evalOracle{hdr: hashOnly}, 500, cfg)
	require.Equal(t, types.SyncState_SYNCED, synced)

	catch := heightsync.EvaluateSyncState(ctx, &evalOracle{hdr: hashOnly}, 510, cfg)
	require.Equal(t, types.SyncState_CATCHING_UP, catch, "CATCHING_UP is reported; Strong is not required")

	stale := heightsync.EvaluateSyncState(ctx, &evalOracle{hdr: hashOnly, stale: true}, 500, cfg)
	require.Equal(t, types.SyncState_ORACLE_STALE, stale)

	down := heightsync.EvaluateSyncState(ctx, &evalOracle{err: context.Canceled}, 500, cfg)
	require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, down)

	require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, heightsync.EvaluateSyncState(ctx, nil, 500, cfg))
}

func TestEvaluateSyncStateFromHeader_DoesNotCallLatest(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hashOnly := blocks.HashOnlyHeader(500, time.Unix(1, 0).UTC(), "gonka-test", []byte{1, 2, 3})
	o := &evalOracle{hdr: hashOnly}

	st := heightsync.EvaluateSyncStateFromHeader(o, hashOnly, nil, 500, cfg)
	require.Equal(t, types.SyncState_SYNCED, st)
	require.Equal(t, 0, o.latestCalls)

	st = heightsync.EvaluateSyncState(context.Background(), o, 500, cfg)
	require.Equal(t, types.SyncState_SYNCED, st)
	require.Equal(t, 1, o.latestCalls)
}

func TestHeartbeat_HashOnlyOracle_TurnCompletes(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hashOnly := blocks.HashOnlyHeader(500, time.Unix(1, 0).UTC(), "gonka-test", []byte{9, 9})
	require.Empty(t, hashOnly.Commit.Signatures)
	o := &evalOracle{hdr: hashOnly}

	st := heightsync.EvaluateSyncState(context.Background(), o, 500, cfg)
	require.Equal(t, types.SyncState_SYNCED, st)
	require.False(t, o.proveCalled, "hash-only heartbeat must not request Strong / Prove")

	tr := heightsync.NewTurnTracker(4, 3, cfg)
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(10, 0, 500, st),
		ackTx(10, 1, 500, st),
		ackTx(10, 2, 500, st),
	}, 500)
	require.Equal(t, heightsync.TurnComplete, tr.Record(10).State)
	require.False(t, o.proveCalled)
}

func TestPeerSeen_BitmapAndExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ps := heightsync.NewPeerSeen(4, time.Second)
	ps.MarkFresh(1, 50, now)
	ps.MarkFresh(3, 51, now)
	b := ps.BytesAt(now)
	require.Equal(t, byte(1<<1|1<<3), b[0])

	expired := ps.BytesAt(now.Add(2 * time.Second))
	require.Equal(t, byte(0), expired[0])
}

func TestComposeSyncVector_ReportsPreviousTurn(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{heartbeatTx(500, 4)}, 500)
	tr.Observe(14, []*types.DevshardTx{
		ackTx(10, 0, 500, types.SyncState_SYNCED),
		ackTx(10, 2, 501, types.SyncState_SYNCED),
	}, 501)
	vec := heightsync.ComposeSyncVector(4, tr.Record(10))
	require.Len(t, vec, 4)
	require.Equal(t, types.AckStatus_ACKED, vec[0].Status)
	require.Equal(t, types.AckStatus_MISSING, vec[1].Status)
	require.Equal(t, types.AckStatus_ACKED, vec[2].Status)

	logAcks := tr.Record(10).Acks
	require.Empty(t, heightsync.CheckVectorAgainstLog(vec, logAcks))

	vec[1].Status = types.AckStatus_ACKED
	vec[1].AckNonce = 99
	got := heightsync.CheckVectorAgainstLog(vec, logAcks)
	require.Len(t, got, 1)
	require.Equal(t, uint32(1), got[0].Slot)
}

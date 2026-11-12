package registry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/user"
)

const (
	floorChainID          = "fake-chain"
	floorBlockTimeSecond  = 1
	floorBlockHashMarker  = 0xaa
	floorClockEpochSecond = 1_000_000
	floorHeartbeatCadence = 10
	floorRaiseAttempts    = 4
	floorBootstrapStarted = 1000
	floorBootstrapInput   = 100
)

type floorOracle struct{ height atomic.Int64 }

func (o *floorOracle) Latest(context.Context) (*blocks.Header, error) {
	return blocks.HashOnlyHeader(o.height.Load(), time.Unix(floorBlockTimeSecond, 0).UTC(), floorChainID, []byte{floorBlockHashMarker}), nil
}

func (o *floorOracle) At(ctx context.Context, _ int64) (*blocks.Header, error) { return o.Latest(ctx) }

func (o *floorOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *floorOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	closed := make(chan *blocks.Header)
	close(closed)
	return closed, nil
}

type floorRig struct {
	height  atomic.Uint64
	clock   atomic.Int64
	oracles []*floorOracle
}

func newFloorRig(hosts int, height uint64) *floorRig {
	rig := &floorRig{oracles: make([]*floorOracle, hosts)}
	rig.height.Store(height)
	rig.clock.Store(time.Unix(floorClockEpochSecond, 0).UnixNano())
	for index := range rig.oracles {
		rig.oracles[index] = &floorOracle{}
		rig.oracles[index].height.Store(int64(height))
	}
	return rig
}

func (r *floorRig) sessionOptions(hosts int) []user.SessionOption {
	return []user.SessionOption{
		user.WithHeightSyncCadence(floorHeartbeatCadence, uint64(hosts)),
		user.WithHeartbeatClock(func() time.Time { return time.Unix(0, r.clock.Load()) }),
		user.WithObservedHeight(func() (uint64, []byte, bool) { return r.height.Load(), []byte{floorBlockHashMarker}, true }),
	}
}

func (r *floorRig) seedFloor(t *testing.T, session *user.Session) {
	t.Helper()
	_, err := session.SendInference(context.Background(), user.InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt, InputLength: floorBootstrapInput, MaxTokens: testutil.TestMaxTokens, StartedAt: floorBootstrapStarted,
	})
	require.NoError(t, err)
	require.NoError(t, session.SendPendingDiff(context.Background()))
	floor, _, known := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.True(t, known)
	require.NotZero(t, floor, "the bootstrap inference must leave a host-seeded floor")
}

func (r *floorRig) raise(t *testing.T, session *user.Session, height uint64) {
	t.Helper()
	r.height.Store(height)
	for _, oracle := range r.oracles {
		oracle.height.Store(int64(height))
	}
	for range floorRaiseAttempts {
		r.clock.Add(int64(time.Hour))
		require.NoError(t, session.MaybeHeartbeat(context.Background()))
		require.NoError(t, session.SendPendingDiff(context.Background()))
		if floor, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1); floor >= height {
			return
		}
	}
	floor, _, _ := session.StateMachine().HeightSyncFloorAsOf(session.Nonce() + 1)
	require.GreaterOrEqual(t, floor, height, "host acks must carry the floor to the raised height")
}

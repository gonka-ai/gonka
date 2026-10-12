package session

import (
	"common/chain"
	"context"
	devshardpkg "devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/transport"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func retryTestPhase(epoch uint64) *chain.Phase {
	p := new(chain.Phase)
	p.SetEpoch(epoch)
	return p
}

func newRetryTestServer(t *testing.T, epoch uint64, records map[uint64]*types.InferenceRecord) *transport.Server {
	t.Helper()
	signer, executor, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{signer, executor})
	cfg := testutil.DefaultConfig(2)
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), cfg, group, 100000)
	sm, err := state.NewStateMachine("escrow-1", cfg, group, 100000, user.Address(), signing.NewSecp256k1Verifier(), store)
	require.NoError(t, err)
	snapshot := sm.SnapshotState()
	for id, rec := range records {
		snapshot.Inferences[id] = rec
	}
	sm.RestoreState(&snapshot)
	h, err := host.NewHost(sm, signer, stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithEpochID(epoch))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	srv, err := transport.NewServer(h, store, signing.NewSecp256k1Verifier(), user.Address())
	require.NoError(t, err)
	return srv
}

func TestRetryEpochAndChallenge(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		current, hostEpoch, leaseEpoch uint64
		status                         types.InferenceStatus
		advance                        bool
		wantVote, wantSkipped          bool
	}{
		{name: "challenged", current: 416, hostEpoch: 416, leaseEpoch: 416, status: types.StatusChallenged, wantVote: true},
		{name: "expired wrong lease", current: 417, hostEpoch: 416, leaseEpoch: 417, status: types.StatusChallenged},
		{name: "unknown epoch", hostEpoch: 416, leaseEpoch: 416, status: types.StatusChallenged},
		{name: "future epoch", current: 415, hostEpoch: 416, leaseEpoch: 416, status: types.StatusChallenged},
		{name: "wrong lease in current escrow", current: 416, hostEpoch: 416, leaseEpoch: 417, status: types.StatusChallenged, wantSkipped: true},
		{name: "epoch advances during ML", current: 416, hostEpoch: 416, leaseEpoch: 416, status: types.StatusChallenged, advance: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetryTestServer(t, tc.hostEpoch, map[uint64]*types.InferenceRecord{5117: {Status: tc.status, ExecutorSlot: 1}})
			phase := retryTestPhase(tc.current)
			calls := 0
			engine := &stubEngine{validateFn: func(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
				calls++
				if tc.advance {
					phase.SetEpoch(tc.current + 1)
				}
				return &devshardpkg.ValidateResult{Valid: true}, nil
			}}
			leases := &stubStaleLeaseStore{}
			loop := &RetryLoop{phase: phase, manager: creditRetryManager{srv}, inner: engine, leases: leases, leaseTTL: time.Hour}
			err := loop.retryOne(context.Background(), "escrow-1", 5117, tc.leaseEpoch)
			if tc.current != tc.hostEpoch || tc.current == 0 || tc.advance {
				require.ErrorIs(t, err, devshardpkg.ErrValidationEpochUnavailable)
			} else {
				require.NoError(t, err)
			}
			if tc.wantVote {
				require.Equal(t, 1, calls)
				require.Len(t, srv.Host().MempoolTxs(), 1)
				require.NotNil(t, srv.Host().MempoolTxs()[0].GetValidationVote())
			} else {
				require.Empty(t, srv.Host().MempoolTxs())
				if !tc.advance {
					require.Zero(t, calls)
				}
			}
			if tc.wantVote || tc.wantSkipped {
				require.Len(t, leases.setResultCalls, 1)
			} else {
				require.Empty(t, leases.setResultCalls)
			}
		})
	}
}

func TestRetryExpiredEscrowDoesNotClaim(t *testing.T) {
	srv := newRetryTestServer(t, 416, nil)
	leases := &stubStaleLeaseStore{acquireFn: func(context.Context, string, string, time.Duration) (uint64, uint64, error) {
		t.Fatal("must not claim")
		return 0, 0, nil
	}}
	loop := &RetryLoop{manager: creditRetryManager{srv}, leases: leases, phase: retryTestPhase(417)}
	loop.retryForEscrow(context.Background(), "escrow-1")
}

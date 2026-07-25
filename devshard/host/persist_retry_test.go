package host

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type flakyHostAppendStore struct {
	storage.Storage
	failsLeft atomic.Int32
	calls     atomic.Int32
}

func (s *flakyHostAppendStore) AppendDiff(escrowID string, rec types.DiffRecord) error {
	s.calls.Add(1)
	if s.failsLeft.Add(-1) >= 0 {
		return errors.New("transient append failure")
	}
	return s.Storage.AppendDiff(escrowID, rec)
}

func signNextStartDiff(t *testing.T, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, store storage.Storage) types.Diff {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)
	txs := []*types.DevshardTx{testutil.StartTx(1)}
	root, err := sm.ApplyLocal(1, txs)
	require.NoError(t, err)
	return testutil.SignDiffWithRoot(t, user, "escrow-1", 1, txs, root)
}

func TestHost_PersistRetryThenSuccess(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &flakyHostAppendStore{Storage: inner}
	store.failsLeft.Store(2)

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 10000,
	}))

	diff := signNextStartDiff(t, user, hosts, inner)
	sm0, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, inner)
	require.NoError(t, err)
	h, err := NewHost(sm0, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	beforeRetry := promtestutil.ToFloat64(observability.DiffPersistRetryForTest("success"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = h.HandleRequest(ctx, HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.Equal(t, uint64(1), h.LatestNonce())
	require.Equal(t, int32(3), store.calls.Load())
	require.Equal(t, beforeRetry+1, promtestutil.ToFloat64(observability.DiffPersistRetryForTest("success")))

	meta, err := inner.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

func TestHost_PersistFirst_FailureLeavesMemoryUnchanged(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	inner := storage.NewMemory()
	store := &flakyHostAppendStore{Storage: inner}
	store.failsLeft.Store(100)

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, inner.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 10000,
	}))

	diff := signNextStartDiff(t, user, hosts, inner)
	sm0, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, inner)
	require.NoError(t, err)
	h, err := NewHost(sm0, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	beforeExhausted := promtestutil.ToFloat64(observability.DiffPersistRetryForTest("exhausted"))
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.Error(t, err)
	require.ErrorIs(t, err, storage.ErrPersistExhausted)
	require.Equal(t, uint64(0), h.LatestNonce(), "persist-first must not advance memory on persist fail")
	require.Equal(t, int32(storage.DefaultPersistMaxAttempts), store.calls.Load())
	require.Equal(t, beforeExhausted+1, promtestutil.ToFloat64(observability.DiffPersistRetryForTest("exhausted")))

	meta, err := inner.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(0), meta.LatestNonce)

	// Same host, heal store, retry same nonce — no reload required.
	store.failsLeft.Store(0)
	store.calls.Store(0)
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.Equal(t, uint64(1), h.LatestNonce())
	meta, err = inner.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

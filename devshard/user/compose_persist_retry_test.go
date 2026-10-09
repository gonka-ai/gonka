package user

import (
	"errors"
	"sync/atomic"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type flakyUserAppendStore struct {
	storage.Storage
	failsLeft atomic.Int32
	calls     atomic.Int32
}

func (s *flakyUserAppendStore) AppendDiff(escrowID string, rec types.DiffRecord) error {
	s.calls.Add(1)
	if s.failsLeft.Add(-1) >= 0 {
		return errors.New("transient append failure")
	}
	return s.Storage.AppendDiff(escrowID, rec)
}

func setupStoredSession(t *testing.T, store storage.Storage) *Session {
	t.Helper()
	numHosts := 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		CreatorAddr:    user.Address(),
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	clients := make([]HostClient, numHosts)
	for i := range hosts {
		hostStore := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000)
		sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, hostStore)
		require.NoError(t, err)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, store)
	require.NoError(t, err)
	session, err := NewSession(userSM, user, "escrow-1", group, clients, verifier, WithStorage(store))
	require.NoError(t, err)
	return session
}

func TestSession_ComposeDiff_PersistRetryThenSuccess(t *testing.T) {
	inner := storage.NewMemory()
	store := &flakyUserAppendStore{Storage: inner}
	store.failsLeft.Store(2)
	session := setupStoredSession(t, store)

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	beforeRetry := promtestutil.ToFloat64(observability.DiffPersistRetryForTest("success"))
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Equal(t, uint64(1), session.Nonce())
	require.Equal(t, int32(3), store.calls.Load())
	require.Equal(t, beforeRetry+1, promtestutil.ToFloat64(observability.DiffPersistRetryForTest("success")))

	meta, err := inner.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

func TestSession_ComposeDiff_PersistFirst_FailureLeavesSequencerUnchanged(t *testing.T) {
	inner := storage.NewMemory()
	store := &flakyUserAppendStore{Storage: inner}
	store.failsLeft.Store(100)
	session := setupStoredSession(t, store)

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	beforeExhausted := promtestutil.ToFloat64(observability.DiffPersistRetryForTest("exhausted"))
	_, err := session.PrepareInference(params)
	require.Error(t, err)
	require.ErrorIs(t, err, storage.ErrPersistExhausted)
	require.Equal(t, uint64(0), session.Nonce(), "persist-first must not advance sequencer on persist fail")
	require.Equal(t, int32(storage.DefaultPersistMaxAttempts), store.calls.Load())
	require.Equal(t, beforeExhausted+1, promtestutil.ToFloat64(observability.DiffPersistRetryForTest("exhausted")))

	meta, err := inner.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(0), meta.LatestNonce)

	// Same session, heal store, re-compose allocates nonce 1 and persists — no reload.
	store.failsLeft.Store(0)
	store.calls.Store(0)
	prepared, err := session.PrepareInference(params)
	require.NoError(t, err)
	require.Equal(t, uint64(1), prepared.diff.Nonce)
	require.Equal(t, uint64(1), session.Nonce())
	meta, err = inner.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.LatestNonce)
}

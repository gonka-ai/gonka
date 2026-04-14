package host

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/stub"
	"subnet/types"
)

// recoverySigStore wraps Memory so computeFinalizedNonce can see BFT signatures that were
// gossiped before replay without requiring a placeholder AppendDiff for the same nonce
// (which would block replay AppendDiff).
type recoverySigStore struct {
	inner *storage.Memory
	// preSig supplies GetSignatures when inner has no diff row yet for that nonce.
	preSig map[uint64]map[uint32][]byte
}

func newRecoverySigStore(inner *storage.Memory) *recoverySigStore {
	return &recoverySigStore{inner: inner, preSig: make(map[uint64]map[uint32][]byte)}
}

func (s *recoverySigStore) setPreSignatures(nonce uint64, slots []uint32) {
	m := make(map[uint32][]byte, len(slots))
	for _, slot := range slots {
		m[slot] = []byte{1}
	}
	s.preSig[nonce] = m
}

func copySigMap(src map[uint32][]byte) map[uint32][]byte {
	if src == nil {
		return nil
	}
	dst := make(map[uint32][]byte, len(src))
	for k, v := range src {
		dst[k] = append([]byte(nil), v...)
	}
	return dst
}

func (s *recoverySigStore) CreateSession(p storage.CreateSessionParams) error {
	return s.inner.CreateSession(p)
}

func (s *recoverySigStore) MarkSettled(escrowID string) error {
	return s.inner.MarkSettled(escrowID)
}

func (s *recoverySigStore) ListActiveSessions() ([]string, error) {
	return s.inner.ListActiveSessions()
}

func (s *recoverySigStore) AppendDiff(escrowID string, rec types.DiffRecord) error {
	return s.inner.AppendDiff(escrowID, rec)
}

func (s *recoverySigStore) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	return s.inner.GetDiffs(escrowID, fromNonce, toNonce)
}

func (s *recoverySigStore) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	return s.inner.AddSignature(escrowID, nonce, slotID, sig)
}

func (s *recoverySigStore) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	m, err := s.inner.GetSignatures(escrowID, nonce)
	if err == nil {
		return m, nil
	}
	if overlay, ok := s.preSig[nonce]; ok {
		return copySigMap(overlay), nil
	}
	return nil, err
}

func (s *recoverySigStore) GetSessionMeta(escrowID string) (*storage.SessionMeta, error) {
	return s.inner.GetSessionMeta(escrowID)
}

func (s *recoverySigStore) MarkFinalized(escrowID string, nonce uint64) error {
	return s.inner.MarkFinalized(escrowID, nonce)
}

func (s *recoverySigStore) LastFinalized(escrowID string) (uint64, error) {
	return s.inner.LastFinalized(escrowID)
}

func (s *recoverySigStore) Close() error {
	return s.inner.Close()
}

func txStartInference(msg *types.MsgStartInference) *types.SubnetTx {
	return &types.SubnetTx{Tx: &types.SubnetTx_StartInference{StartInference: msg}}
}

func txConfirmStart(msg *types.MsgConfirmStart) *types.SubnetTx {
	return &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: msg}}
}

func newRecoveryTestHost(
	t *testing.T,
	hostIdx int,
	hosts []*signing.Secp256k1Signer,
	user *signing.Secp256k1Signer,
	store storage.Storage,
	resolver state.WarmKeyResolver,
) *Host {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, state.WithWarmKeyResolver(resolver))
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[hostIdx], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)
	return h
}

// Finalized nonce with WarmKeyDelta on the wire: InjectWarmKeys path applies bindings so the
// warm-key resolver is never consulted.
func TestApplyRecoveredDiffs_injectWarmKeysWhenFinalizedNoMainnet(t *testing.T) {
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	warmSigner := testutil.MustGenerateKey(t)
	executorSlot := uint32(1 % 4)

	var resolverCalls atomic.Uint32
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		resolverCalls.Add(1)
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorSlot].Address(), nil
	}

	mem := storage.NewMemory()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, Config: config, Group: group, InitialBalance: 10000,
	}))
	store := newRecoverySigStore(mem)
	// 3/4 slots signed at nonce 1 → F = 1.
	store.setPreSignatures(1, []uint32{0, 1, 2})

	promptHash := []byte("prompt")
	execSig := testutil.SignExecutorReceipt(t, warmSigner, escrowID, 1, promptHash, "llama", 100, 50, 1000, 1000)
	txs := []*types.SubnetTx{
		txStartInference(&types.MsgStartInference{
			InferenceId: 1, PromptHash: promptHash, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}),
		txConfirmStart(&types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
		}),
	}
	diff := testutil.SignDiff(t, user, escrowID, 1, txs)
	rec := types.DiffRecord{
		Diff:         diff,
		WarmKeyDelta: map[uint32]string{executorSlot: warmSigner.Address()},
	}

	h := newRecoveryTestHost(t, 0, hosts, user, store, resolver)
	sigs, err := h.ApplyRecoveredDiffs(context.Background(), []types.DiffRecord{rec})
	require.NoError(t, err)
	require.NotEmpty(t, sigs)
	require.Equal(t, uint32(0), resolverCalls.Load(), "BFT-committed warm keys must not hit mainnet resolver")
}

// Non-finalized nonce: WarmKeyDelta is not injected; ApplyDiff must resolve via the bridge callback.
func TestApplyRecoveredDiffs_resolveWarmKeyWhenAboveF(t *testing.T) {
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	warmSigner := testutil.MustGenerateKey(t)
	executorSlot := uint32(1 % 4)

	var resolverCalls atomic.Uint32
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		resolverCalls.Add(1)
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorSlot].Address(), nil
	}

	mem := storage.NewMemory()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, Config: config, Group: group, InitialBalance: 10000,
	}))
	store := newRecoverySigStore(mem)
	// F = 1: supermajority at nonce 1 only; nonce 2 lacks 2/3+ → cannot raise F past 1.
	store.setPreSignatures(1, []uint32{0, 1, 2})
	store.setPreSignatures(2, []uint32{0})

	promptHash := []byte("prompt")
	diff1 := testutil.SignDiff(t, user, escrowID, 1, []*types.SubnetTx{
		txStartInference(&types.MsgStartInference{
			InferenceId: 1, PromptHash: promptHash, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}),
	})
	execSig := testutil.SignExecutorReceipt(t, warmSigner, escrowID, 1, promptHash, "llama", 100, 50, 1000, 1000)
	diff2 := testutil.SignDiff(t, user, escrowID, 2, []*types.SubnetTx{
		txConfirmStart(&types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
		}),
	})
	delta := map[uint32]string{executorSlot: warmSigner.Address()}

	h := newRecoveryTestHost(t, 0, hosts, user, store, resolver)
	sigs, err := h.ApplyRecoveredDiffs(context.Background(), []types.DiffRecord{
		{Diff: diff1},
		{Diff: diff2, WarmKeyDelta: delta},
	})
	require.NoError(t, err)
	require.NotEmpty(t, sigs)
	require.GreaterOrEqual(t, resolverCalls.Load(), uint32(1),
		"nonce 2 > F should resolve warm keys via mainnet callback")
}

// Warm-up period (F = 0): every nonce is above finalized; warm bindings are never injected from the wire.
func TestApplyRecoveredDiffs_fZeroUsesResolver(t *testing.T) {
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	warmSigner := testutil.MustGenerateKey(t)
	executorSlot := uint32(1 % 4)

	var resolverCalls atomic.Uint32
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		resolverCalls.Add(1)
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorSlot].Address(), nil
	}

	mem := storage.NewMemory()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, Config: config, Group: group, InitialBalance: 10000,
	}))
	store := newRecoverySigStore(mem)
	// Only one slot at nonce 1 → F = 0.
	store.setPreSignatures(1, []uint32{0})

	promptHash := []byte("prompt")
	txs := []*types.SubnetTx{
		txStartInference(&types.MsgStartInference{
			InferenceId: 1, PromptHash: promptHash, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}),
		txConfirmStart(&types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: testutil.SignExecutorReceipt(t, warmSigner, escrowID, 1, promptHash, "llama", 100, 50, 1000, 1000),
			ConfirmedAt: 1000,
		}),
	}
	diff := testutil.SignDiff(t, user, escrowID, 1, txs)
	rec := types.DiffRecord{
		Diff:         diff,
		WarmKeyDelta: map[uint32]string{executorSlot: warmSigner.Address()},
	}

	h := newRecoveryTestHost(t, 0, hosts, user, store, resolver)
	_, err := h.ApplyRecoveredDiffs(context.Background(), []types.DiffRecord{rec})
	require.NoError(t, err)
	require.GreaterOrEqual(t, resolverCalls.Load(), uint32(1), "F=0 must fall back to ResolveWarmKey")
}

// Finalized nonce but empty WarmKeyDelta: inject is skipped; warm tx still needs the resolver.
func TestApplyRecoveredDiffs_emptyWarmKeyDeltaSkipsInject(t *testing.T) {
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	warmSigner := testutil.MustGenerateKey(t)
	executorSlot := uint32(1 % 4)

	var resolverCalls atomic.Uint32
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		resolverCalls.Add(1)
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorSlot].Address(), nil
	}

	mem := storage.NewMemory()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, Config: config, Group: group, InitialBalance: 10000,
	}))
	store := newRecoverySigStore(mem)
	store.setPreSignatures(1, []uint32{0, 1, 2})

	promptHash := []byte("prompt")
	txs := []*types.SubnetTx{
		txStartInference(&types.MsgStartInference{
			InferenceId: 1, PromptHash: promptHash, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}),
		txConfirmStart(&types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: testutil.SignExecutorReceipt(t, warmSigner, escrowID, 1, promptHash, "llama", 100, 50, 1000, 1000),
			ConfirmedAt: 1000,
		}),
	}
	diff := testutil.SignDiff(t, user, escrowID, 1, txs)
	rec := types.DiffRecord{Diff: diff, WarmKeyDelta: nil}

	h := newRecoveryTestHost(t, 0, hosts, user, store, resolver)
	_, err := h.ApplyRecoveredDiffs(context.Background(), []types.DiffRecord{rec})
	require.NoError(t, err)
	require.GreaterOrEqual(t, resolverCalls.Load(), uint32(1),
		"without injected delta, ConfirmStart must use ResolveWarmKey")
}

// Bad WarmKeyDelta followed by apply failure must not leave poisoned warm keys (retry-safe).
func TestApplyRecoveredDiffs_rollbackWarmKeysOnApplyFailure(t *testing.T) {
	const escrowID = "escrow-1"
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t), testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	warmSigner := testutil.MustGenerateKey(t)
	executorSlot := uint32(1 % 4)

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorSlot].Address(), nil
	}

	mem := storage.NewMemory()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID, Config: config, Group: group, InitialBalance: 10000,
	}))
	store := newRecoverySigStore(mem)
	store.setPreSignatures(1, []uint32{0, 1, 2})

	promptHash := []byte("prompt")
	txs := []*types.SubnetTx{
		txStartInference(&types.MsgStartInference{
			InferenceId: 1, PromptHash: promptHash, Model: "llama",
			InputLength: 100, MaxTokens: 50, StartedAt: 1000,
		}),
		txConfirmStart(&types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: testutil.SignExecutorReceipt(t, warmSigner, escrowID, 1, promptHash, "llama", 100, 50, 1000, 1000),
			ConfirmedAt: 1000,
		}),
	}
	diff := testutil.SignDiff(t, user, escrowID, 1, txs)
	diff.UserSig[0] ^= 0xff

	rec := types.DiffRecord{
		Diff:         diff,
		WarmKeyDelta: map[uint32]string{executorSlot: warmSigner.Address()},
	}

	h := newRecoveryTestHost(t, 0, hosts, user, store, resolver)
	_, err := h.ApplyRecoveredDiffs(context.Background(), []types.DiffRecord{rec})
	require.Error(t, err)
	require.Empty(t, h.sm.WarmKeys())
}

package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

func makeKeys(t *testing.T, n int) []*signing.Secp256k1Signer {
	t.Helper()
	keys := make([]*signing.Secp256k1Signer, n)
	for i := range keys {
		keys[i] = testutil.MustGenerateKey(t)
	}
	return keys
}

func acceptResolver(warmKeys, coldKeys []*signing.Secp256k1Signer) state.WarmKeyResolver {
	allowed := make(map[string]string, len(warmKeys))
	for i, w := range warmKeys {
		allowed[w.Address()] = coldKeys[i].Address()
	}
	return func(warmAddr, coldAddr string) (bool, error) {
		if expected, ok := allowed[warmAddr]; ok && expected == coldAddr {
			return true, nil
		}
		return false, nil
	}
}

var defaultParams = InferenceParams{
	Model: "llama", Prompt: testutil.TestPrompt,
	InputLength: 100, MaxTokens: 50, StartedAt: 1000,
}

// setupWarmKeySession creates a session where hosts sign with warm keys
// and the resolver accepts the warm->cold mapping.
func setupWarmKeySession(t *testing.T, n int) (*Session, []*signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	coldKeys := makeKeys(t, n)
	warmKeys := makeKeys(t, n)
	resolver := acceptResolver(warmKeys, coldKeys)

	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldKeys)
	config := testutil.DefaultConfig(n)
	verifier := signing.NewSecp256k1Verifier()
	smOpts := []state.SMOption{state.WithWarmKeyResolver(resolver)}

	clients := make([]HostClient, n)
	for i := range coldKeys {
		sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000), smOpts...)
		require.NoError(t, err)
		engine := stub.NewInferenceEngine()
		h, err := host.NewHost(sm, warmKeys[i], engine, "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &InProcessClient{Host: h}
	}

	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000), smOpts...)
	require.NoError(t, err)
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	return session, coldKeys, warmKeys
}

func TestProcessResponse_WarmKey_Accepted(t *testing.T) {
	session, _, _ := setupWarmKeySession(t, 3)

	ctx := context.Background()
	_, err := session.SendInference(ctx, defaultParams)
	require.NoError(t, err, "warm key should be accepted by resolver")
	require.NotEmpty(t, session.Signatures())

	// State sig verification uses CheckWarmKey (non-mutating), so the
	// user's SM won't have warm keys cached yet. They'll be cached when
	// the next diff applies a tx signed by a warm key (e.g. MsgConfirmStart).
	// Send a second inference to include the receipt from the first.
	_, err = session.SendInference(ctx, defaultParams)
	require.NoError(t, err)

	wk := session.StateMachine().WarmKeys()
	require.NotEmpty(t, wk, "warm key binding should be cached after applying confirm")
}

func TestProcessResponse_WarmKey_Rejected(t *testing.T) {
	// Build a session manually: warm key signs the state sig, but the resolver rejects it.
	coldKeys := makeKeys(t, 3)
	warmKeys := makeKeys(t, 3)
	rejectAll := func(_, _ string) (bool, error) { return false, nil }

	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldKeys)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000),
		state.WithWarmKeyResolver(rejectAll),
	)
	require.NoError(t, err)

	// Apply a diff locally.
	startTx := testutil.StartTx(1)
	root, err := userSM.ApplyLocal(1, []*types.DevshardTx{startTx})
	require.NoError(t, err)

	// Sign state with warm key (which the resolver will reject).
	sigContent := &types.StateSignatureContent{
		StateRoot: root, EscrowId: "escrow-1", Nonce: 1,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)
	stateSig, err := warmKeys[1].Sign(sigData)
	require.NoError(t, err)

	clients := make([]HostClient, 3)
	for i := range clients {
		clients[i] = &ErrorClient{}
	}
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	session.nonce = 1
	session.diffs = append(session.diffs, types.Diff{Nonce: 1, PostStateRoot: root})

	err = session.ProcessResponse(1, &host.HostResponse{
		Nonce: 1, StateHash: root, StateSig: stateSig,
	}, 1)
	require.NoError(t, err, "rejected warm key is skipped, not fatal to the response")
	require.Empty(t, session.signatures[1], "rejected warm key must not count toward quorum")
}

func TestProcessResponse_WarmKey_NoResolver(t *testing.T) {
	// No resolver -- warm key mismatch should fail.
	coldKeys := makeKeys(t, 3)
	warmKeys := makeKeys(t, 3)

	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldKeys)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	// No warm key resolver option.
	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000))
	require.NoError(t, err)

	startTx := testutil.StartTx(1)
	root, err := userSM.ApplyLocal(1, []*types.DevshardTx{startTx})
	require.NoError(t, err)

	sigContent := &types.StateSignatureContent{
		StateRoot: root, EscrowId: "escrow-1", Nonce: 1,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)
	stateSig, err := warmKeys[1].Sign(sigData)
	require.NoError(t, err)

	clients := make([]HostClient, 3)
	for i := range clients {
		clients[i] = &ErrorClient{}
	}
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	session.nonce = 1
	session.diffs = append(session.diffs, types.Diff{Nonce: 1, PostStateRoot: root})

	err = session.ProcessResponse(1, &host.HostResponse{
		Nonce: 1, StateHash: root, StateSig: stateSig,
	}, 1)
	require.NoError(t, err, "without resolver, warm key mismatch is skipped, not fatal")
	require.Empty(t, session.signatures[1], "unresolved warm key must not count toward quorum")
}

func TestProcessResponse_ColdKey_StillWorks(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)

	_, err := session.SendInference(context.Background(), defaultParams)
	require.NoError(t, err, "cold key should work without warm key resolver")
	require.NotEmpty(t, session.Signatures())
}

// A1: an unverifiable state sig must not drop the executor's receipt/finish — the nonce still finishes and its txs are queued.
func TestProcessResponse_InvalidStateSig_StillProcessesReceiptAndFinish(t *testing.T) {
	coldKeys := makeKeys(t, 3)
	warmKeys := makeKeys(t, 3)
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(coldKeys)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	userSM, err := state.NewStateMachine("escrow-1", config, group, 100000, userKey.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userKey.Address(), config, group, 100000))
	require.NoError(t, err)

	root, err := userSM.ApplyLocal(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.NoError(t, err)

	sigData, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: "escrow-1", Nonce: 1})
	require.NoError(t, err)
	badSig, err := warmKeys[1].Sign(sigData) // recovers to a warm key with no resolver -> rejected
	require.NoError(t, err)

	clients := make([]HostClient, 3)
	for i := range clients {
		clients[i] = &ErrorClient{}
	}
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	session.nonce = 1
	session.diffs = append(session.diffs, types.Diff{Nonce: 1, PostStateRoot: root})
	session.nonceStates[1] = &nonceOutcome{}

	finishTx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: 1},
	}}
	err = session.ProcessResponse(1, &host.HostResponse{
		Nonce:     1,
		StateHash: root,
		StateSig:  badSig,
		Receipt:   []byte("receipt"),
		Mempool:   []*types.DevshardTx{finishTx},
	}, 1)

	require.NoError(t, err, "invalid state sig must not drop the response")
	require.Empty(t, session.signatures[1], "invalid sig must not count toward quorum")
	require.True(t, session.IsNonceFinished(1), "finish must still be observed after a skipped sig")
	require.NotEmpty(t, session.PendingTxs(), "receipt and finish must still be queued")
}

func TestProcessResponse_WarmKey_Finalize(t *testing.T) {
	session, _, _ := setupWarmKeySession(t, 3)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := session.SendInference(ctx, defaultParams)
		require.NoError(t, err)
	}

	err := session.Finalize(ctx)
	require.NoError(t, err, "finalize should succeed with warm keys")

	st := session.StateMachine().SnapshotState()
	require.True(t, st.Phase >= types.PhaseFinalizing)
}

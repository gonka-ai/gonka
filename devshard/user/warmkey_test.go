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

// warmGrants is a grant source where coldKeys[i] granted warmKeys[i].
func warmGrants(warmKeys, coldKeys []*signing.Secp256k1Signer) *testutil.WarmKeyGrants {
	grants := testutil.NewWarmKeyGrants()
	for i, w := range warmKeys {
		grants.Grant(coldKeys[i].Address(), w.Address())
	}
	return grants
}

func acceptResolver(warmKeys, coldKeys []*signing.Secp256k1Signer) state.WarmKeyResolver {
	return warmGrants(warmKeys, coldKeys).Resolver
}

var defaultParams = InferenceParams{
	Model: "llama", Prompt: testutil.TestPrompt,
	InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
}

// setupWarmKeySession creates a session where hosts sign with warm keys, the
// grant source knows the warm->cold grants and the sequencer binds them.
func setupWarmKeySession(t *testing.T, n int) (*Session, []*signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	coldKeys := makeKeys(t, n)
	warmKeys := makeKeys(t, n)
	grants := warmGrants(warmKeys, coldKeys)
	resolver := grants.Resolver

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
	session, err := NewSession(userSM, userKey, "escrow-1", group, clients, verifier, WithWarmKeyGrantSource(grants))
	require.NoError(t, err)
	return session, coldKeys, warmKeys
}

func TestProcessResponse_WarmKey_Accepted(t *testing.T) {
	session, _, warmKeys := setupWarmKeySession(t, 3)

	ctx := context.Background()
	_, err := session.SendInference(ctx, defaultParams)
	require.NoError(t, err, "warm key should be accepted by resolver")
	require.NotEmpty(t, session.Signatures())

	// The first diff carried the binds the sequencer proposed from the
	// grant source, one per slot.
	history := session.StateMachine().WarmKeyHistory()
	require.Len(t, history, 3, "the sequencer binds every live grant of the group")
	for i := range warmKeys {
		require.Equal(t, []string{warmKeys[i].Address()}, history.Live(uint32(i)))
		require.Equal(t, uint64(1), history[uint32(i)][0].FromNonce)
	}

	_, err = session.SendInference(ctx, defaultParams)
	require.NoError(t, err)
	require.Equal(t, history, session.StateMachine().WarmKeyHistory(), "nothing to propose once every grant is bound")
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
	require.Error(t, err, "rejected warm key should cause error")
	require.ErrorIs(t, err, types.ErrInvalidStateSig)
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
	require.Error(t, err, "without resolver, warm key mismatch should fail")
	require.ErrorIs(t, err, types.ErrInvalidStateSig)
}

func TestProcessResponse_ColdKey_StillWorks(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)

	_, err := session.SendInference(context.Background(), defaultParams)
	require.NoError(t, err, "cold key should work without warm key resolver")
	require.NotEmpty(t, session.Signatures())
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

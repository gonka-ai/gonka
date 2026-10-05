package user

import (
	"context"
	"testing"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func signRootForTest(t *testing.T, signer *signing.Secp256k1Signer, escrowID string, nonce uint64, root []byte) []byte {
	t.Helper()
	data, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: escrowID, Nonce: nonce})
	require.NoError(t, err)
	sig, err := signer.Sign(data)
	require.NoError(t, err)
	return sig
}

func signSessionHostRootForTest(t *testing.T, session *Session, hostIdx int, nonce uint64, root []byte) []byte {
	t.Helper()
	data, err := proto.Marshal(&types.StateSignatureContent{StateRoot: root, EscrowId: session.escrowID, Nonce: nonce})
	require.NoError(t, err)
	sig, err := session.clients[hostIdx].(*InProcessClient).Host.Signer().Sign(data)
	require.NoError(t, err)
	return sig
}

func sessionWithRootForTest(t *testing.T, hosts int) (*Session, []*signing.Secp256k1Signer, uint64, []byte) {
	t.Helper()
	session, signers, _ := setupSession(t, hosts, 100000, 10)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(context.Background(), params)
	require.NoError(t, err)
	nonce := session.Nonce()
	root := session.Diffs()[0].PostStateRoot
	require.Len(t, root, 32)
	return session, signers, nonce, root
}

func TestProcessResponseRejectsSignatureOverDifferentRoot(t *testing.T) {
	session, signers, nonce, root := sessionWithRootForTest(t, 3)
	wrong := append([]byte(nil), root...)
	wrong[0] ^= 0xff
	for _, hash := range [][]byte{nil, {1}, wrong} {
		sig := signRootForTest(t, signers[0], session.escrowID, nonce, hash)
		err := session.ProcessResponse(0, &host.HostResponse{Nonce: nonce, StateHash: hash, StateSig: sig}, nonce)
		require.ErrorIs(t, err, types.ErrStateHashMismatch)
		require.Empty(t, session.Signatures()[nonce][0])
	}

	sig := signRootForTest(t, signers[0], session.escrowID, nonce, root)
	err := session.ProcessResponse(0, &host.HostResponse{Nonce: nonce, StateHash: root, StateSig: sig}, nonce)
	require.NoError(t, err)
	require.Equal(t, sig, session.Signatures()[nonce][0])
}

func TestSettlementSignaturesExcludesStoredWrongRoot(t *testing.T) {
	session, signers, nonce, root := sessionWithRootForTest(t, 4)
	for slot := 2; slot < 4; slot++ {
		sig := signRootForTest(t, signers[slot], session.escrowID, nonce, root)
		err := session.ProcessResponse(slot, &host.HostResponse{Nonce: nonce, StateHash: root, StateSig: sig}, nonce)
		require.NoError(t, err)
	}
	poison := signRootForTest(t, signers[0], session.escrowID, nonce, nil)
	session.mu.Lock()
	session.signatures[nonce][0] = poison
	session.mu.Unlock()

	require.True(t, session.HasQuorumAt(nonce))
	sigs, err := session.SettlementSignatures(nonce)
	require.NoError(t, err)
	require.Len(t, sigs, 3)
	require.NotContains(t, sigs, uint32(0))
	entries, _, hasQuorum := session.SignatureStatus()
	require.True(t, hasQuorum)
	require.Equal(t, uint32(3), entries[0].SigWeight)
}

func TestStoredWrongRootCannotProvideQuorum(t *testing.T) {
	session, signers, nonce, root := sessionWithRootForTest(t, 3)
	sig := signRootForTest(t, signers[2], session.escrowID, nonce, root)
	err := session.ProcessResponse(2, &host.HostResponse{Nonce: nonce, StateHash: root, StateSig: sig}, nonce)
	require.NoError(t, err)
	poison := signRootForTest(t, signers[0], session.escrowID, nonce, nil)
	session.mu.Lock()
	session.signatures[nonce][0] = poison
	session.mu.Unlock()

	require.False(t, session.HasQuorumAt(nonce))
	_, err = session.SettlementSignatures(nonce)
	require.ErrorContains(t, err, "insufficient valid signatures: 2/3")
	entries, _, hasQuorum := session.SignatureStatus()
	require.False(t, hasQuorum)
	require.Equal(t, uint32(2), entries[0].SigWeight)
}

func TestRecoveredWrongRootCannotProvideQuorum(t *testing.T) {
	store := newTestStore(t)
	group, signers, user := setupRecoverableSession(t, 3, 1, store)
	recs, err := store.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	root := recs[0].PostStateRoot
	require.Len(t, root, 32)
	require.NoError(t, store.AddSignature("escrow-1", 1, 0, signRootForTest(t, signers[0], "escrow-1", 1, nil)))
	require.NoError(t, store.AddSignature("escrow-1", 1, 2, signRootForTest(t, signers[2], "escrow-1", 1, root)))

	recovered, _, err := RecoverSession(store, user, signing.NewSecp256k1Verifier(), "escrow-1",
		testutil.RuntimeTestVersion, group, buildRecoveryClients(t, signers, group, user))
	require.NoError(t, err)
	require.Len(t, recovered.Signatures()[1], 3)
	require.False(t, recovered.HasQuorumAt(1))
	_, err = recovered.SettlementSignatures(1)
	require.ErrorContains(t, err, "insufficient valid signatures: 2/3")
}

func TestSettlementSignaturesExpandsRepeatedOwnerSlots(t *testing.T) {
	owner := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{owner, other, owner})
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()
	sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, user.Address(), verifier)
	clients := []HostClient{&ErrorClient{}, &ErrorClient{}, &ErrorClient{}}
	session, err := NewSession(sm, user, "escrow-1", group, clients, verifier)
	require.NoError(t, err)
	root, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	session.mu.Lock()
	session.nonce = 1
	session.signatures[1] = map[uint32][]byte{
		0: signRootForTest(t, owner, "escrow-1", 1, root),
		1: signRootForTest(t, other, "escrow-1", 1, root),
	}
	session.mu.Unlock()

	sigs, err := session.SettlementSignatures(1)
	require.NoError(t, err)
	require.Len(t, sigs, 3)
	require.Equal(t, sigs[0], sigs[2])
}

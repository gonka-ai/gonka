package host

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// blockingSigStore holds AddSignature until release is closed.
type blockingSigStore struct {
	storage.Storage
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSigStore) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.Storage.AddSignature(escrowID, nonce, slotID, sig)
}

func TestHost_OwnSignatureWriteDoesNotHoldLock(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()

	mem := storage.NewMemory()
	require.NoError(t, mem.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion,
		Config: config, Group: group, InitialBalance: 10000, CreatorAddr: user.Address(),
	}))
	store := &blockingSigStore{Storage: mem, entered: make(chan struct{}, 1), release: make(chan struct{})}
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, store)
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	type result struct {
		resp *HostResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
		done <- result{resp, err}
	}()

	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("AddSignature was not called")
	}

	// The signature write is in flight; another caller must still get h.mu.
	got := make(chan uint64, 1)
	go func() { got <- h.LatestNonce() }()
	select {
	case n := <-got:
		require.Equal(t, uint64(1), n)
	case <-time.After(2 * time.Second):
		close(store.release)
		t.Fatal("h.mu is held while the own signature is written to the store")
	}

	select {
	case <-done:
		t.Fatal("HandleRequest returned before its signature was stored")
	case <-time.After(50 * time.Millisecond):
	}
	close(store.release)
	res := <-done
	require.NoError(t, res.err)
	resp := res.resp
	sigs, err := mem.GetSignatures("escrow-1", resp.Nonce)
	require.NoError(t, err)
	require.Equal(t, resp.StateSig, sigs[0], "signature is stored before HandleRequest returns")
}

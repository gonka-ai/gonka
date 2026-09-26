package rpcserver

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
	"devshard/transport/rpcpb"
)

func newLiveTransportServer(t *testing.T) (*transport.Server, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))
	h, err := host.NewHost(sm, hostSigner, stub.NewInferenceEngine(), "escrow-1", group, nil,
		host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	srv, err := transport.NewServer(h, store, verifier, userSigner.Address())
	require.NoError(t, err)
	return srv, hostSigner, userSigner
}

func liveSessionLookup(t *testing.T) (SessionLookup, *signing.Secp256k1Signer, *signing.Secp256k1Signer) {
	t.Helper()
	srv, hostSigner, userSigner := newLiveTransportServer(t)
	return AdaptLookup(func(string) (*transport.Server, error) { return srv, nil }), hostSigner, userSigner
}

func TestSessionHandler_RepairInvalidRequesterSigThroughEnvelope(t *testing.T) {
	lookup, hostSigner, _ := liveSessionLookup(t)
	env := newSessionEnvWith(t, lookup, "escrow-1", hostSigner)
	_, err := env.session.RepairHeightSync(context.Background(), withSession(
		connect.NewRequest(env.signedEnvelope(t, "escrow-1", transport.RepairRequestToProto(&heightsync.RepairRequest{
			RequesterSlot: 0,
			RequesterSig:  []byte{0x01},
		}))), env.token))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), heightsync.ErrRepairVerify.Error())
}

func TestGossipHandler_InvalidStateSigThroughEnvelope(t *testing.T) {
	lookup, hostSigner, _ := liveSessionLookup(t)
	env := newSessionEnvWith(t, lookup, "escrow-1", hostSigner, WithGossipService(NewGossipHandler(lookup)))
	_, err := env.gossip.Nonce(context.Background(), withSession(
		connect.NewRequest(env.signedEnvelope(t, "escrow-1", &rpcpb.GossipNonceRequest{
			Nonce:     1,
			StateHash: []byte{1},
			StateSig:  []byte{1, 2, 3},
			SlotId:    0,
		})), env.token))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), transport.ErrGossipInvalidStateSig.Error())
}

package session

import (
	"connectrpc.com/connect"
	"context"
	"devshard/bridge"
	"devshard/signing"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
	"errors"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type replicaKeyStore struct {
	storage.Storage
	secret []byte
	err    error
}

func (s replicaKeyStore) PeerSessionKey(host, version string, id byte) ([]byte, bool, error) {
	if s.err != nil {
		return nil, true, s.err
	}
	key, err := signing.DerivePeerSessionKeyFromSecret(s.secret, host, version, id)
	return key, true, err
}

type coldHostRecorder struct {
	PayloadAuthClient
	address string
}

func (r coldHostRecorder) GetAccountAddress() string { return r.address }
func TestPeerSessionTokensCrossWarmReplicas(t *testing.T) {
	cold, peer := mustGenerateKey(t), mustGenerateKey(t)
	seed := make([]byte, 32)
	seed[0] = 42
	newReplica := func(version string, store storage.Storage) *rpcserver.PeerAuthHandler {
		warm := mustGenerateKey(t)
		br := &mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "9901", CreatorAddress: peer.Address(), Slots: []string{cold.Address()}}}
		mgr := NewHostManager(store, warm, nil, nil, nil, version, br, nil, coldHostRecorder{address: cold.Address()})
		t.Cleanup(func() { _ = mgr.Close() })
		return mgr.peerAuthHandler()
	}
	one := newReplica("v7", replicaKeyStore{Storage: storage.NewMemory(), secret: seed})
	two := newReplica("v7", replicaKeyStore{Storage: storage.NewMemory(), secret: seed})
	require.NotNil(t, one)
	require.NotNil(t, two)
	now := time.Now().Unix()
	nonce := []byte("warm-replica-attach-nonce-0001")
	sig, err := transport.SignAttach(peer, cold.Address(), now, peer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	response, err := one.Attach(rpcserver.WithEscrowID(context.Background(), "9901"), connect.NewRequest(&rpcpb.AttachRequest{PeerAddress: peer.Address(), HostAddress: cold.Address(), Timestamp: now, AttachNonce: nonce, ProtocolVersion: transport.AttachProtocolVersion, Signature: sig}))
	require.NoError(t, err)
	got, ok := two.LookupToken(response.Msg.SessionToken)
	require.True(t, ok)
	require.Equal(t, peer.Address(), got)
	forged := append([]byte(nil), response.Msg.SessionToken...)
	forged[len(forged)-1] ^= 1
	_, ok = two.LookupToken(forged)
	require.False(t, ok)
	wrongSeed := make([]byte, 32)
	wrongSeed[0] = 43
	other := newReplica("v7", replicaKeyStore{Storage: storage.NewMemory(), secret: wrongSeed})
	_, ok = other.LookupToken(response.Msg.SessionToken)
	require.False(t, ok)
	// Configured-store failure disables auth; it must not create signer-local tokens.
	require.Nil(t, newReplica("v7", replicaKeyStore{Storage: storage.NewMemory(), err: errors.New("key unavailable")}))
	// v6 remains signer-derived even when storage offers a shared seed.
	legacy := newReplica("v6", replicaKeyStore{Storage: storage.NewMemory(), secret: seed})
	_, ok = legacy.LookupToken(response.Msg.SessionToken)
	require.False(t, ok)
}

func TestPeerSessionKeyUnavailableRefusesV7Startup(t *testing.T) {
	store := replicaKeyStore{Storage: storage.NewMemory(), err: errors.New("PG unavailable")}
	mgr := NewHostManager(store, mustGenerateKey(t), nil, nil, nil, "v7", nil, nil, nil)
	t.Cleanup(func() { _ = mgr.Close() })
	require.PanicsWithValue(t, "devshard: v7 peer RPC authentication unavailable; refusing startup", func() { mgr.Register(echo.New().Group("")) })
}

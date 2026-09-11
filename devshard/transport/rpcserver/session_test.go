package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

type stubCore struct {
	sigs     map[uint32][]byte
	err      error
	deny     bool
	sawAllow *string
}

func (s stubCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return s.sigs, s.err
}

func (s stubCore) AllowsSender(addr string) bool {
	if s.sawAllow != nil {
		*s.sawAllow = addr
	}
	return !s.deny
}

type stubLookup struct {
	core SessionCore
	err  error
}

func (s stubLookup) SessionServerExisting(string) (SessionCore, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.core, nil
}

type countingLookup struct {
	core SessionCore
	n    *int
}

func (s countingLookup) SessionServerExisting(string) (SessionCore, error) {
	*s.n++
	return s.core, nil
}

func withEscrow(h http.Handler, escrowID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), escrowID)))
	})
}

type sessionEnv struct {
	session rpcpbconnect.SessionServiceClient
	token   []byte
	peer    string
}

func newSessionEnv(t *testing.T, lookup SessionLookup, escrowID string) sessionEnv {
	t.Helper()
	auth := newTestAuth(PeerAuthConfig{})
	mux := NewMux(auth, NewSessionHandler(lookup))
	srv := httptest.NewServer(withEscrow(mux, escrowID))
	t.Cleanup(srv.Close)

	signer := testutil.MustGenerateKey(t)
	authClient := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := []byte("session-handler-attach-012345")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	attached, err := authClient.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	return sessionEnv{
		session: rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL),
		token:   attached.Msg.SessionToken,
		peer:    signer.Address(),
	}
}

func (e sessionEnv) getSignatures(nonce uint64) (*connect.Response[rpcpb.GetSignaturesResponse], error) {
	return e.session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: nonce}), e.token))
}

func TestSessionHandler_GetSignaturesWithoutHandshake(t *testing.T) {
	srv := httptest.NewServer(withEscrow(NewMux(
		newTestAuth(PeerAuthConfig{}),
		NewSessionHandler(stubLookup{core: stubCore{}}),
	), "1"))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err := client.GetSignatures(context.Background(), connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestSessionHandler_GetSignaturesLookupMiss(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: errors.New("no session: storage: disk full at /var/lib/devshard")}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow is not open on this host")
	require.NotContains(t, err.Error(), "storage")
	require.NotContains(t, err.Error(), "disk")
}

func TestSessionHandler_GetSignaturesCoreError(t *testing.T) {
	env := newSessionEnv(t, stubLookup{core: stubCore{err: errors.New("postgres: relation signatures does not exist")}}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Contains(t, err.Error(), "get signatures failed")
	require.NotContains(t, err.Error(), "postgres")
	require.NotContains(t, err.Error(), "relation")
}

func TestSessionHandler_GetSignaturesNilLookup(t *testing.T) {
	env := newSessionEnv(t, nil, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSessionHandler_GetSignaturesCallsCore(t *testing.T) {
	want := []byte{0xde, 0xad}
	env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: want}}}, "1")
	resp, err := env.getSignatures(3)
	require.NoError(t, err)
	require.Equal(t, want, resp.Msg.Signatures[0])
}

func TestSessionHandler_GetSignaturesRosterDenied(t *testing.T) {
	env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}, deny: true}}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestSessionHandler_GetSignaturesEscrowNotOpen(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: errors.New("storage: not found")}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow is not open on this host")
	require.NotContains(t, err.Error(), "storage")
}

func TestSessionHandler_GetSignaturesInitializing(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: fmt.Errorf("escrow 1 is not open on this host: %w", storage.ErrStorageIndexRebuilding)}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host initializing")
	require.NotContains(t, err.Error(), "rebuilding")
	require.NotContains(t, err.Error(), "postgres")
}

func TestSessionHandler_GetSignaturesLookupInitializing(t *testing.T) {
	env := newSessionEnv(t, stubLookup{err: storage.ErrStorageIndexRebuilding}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host initializing")
	require.NotContains(t, err.Error(), "rebuilding")
}

func TestSessionHandler_GetSignaturesNilServer(t *testing.T) {
	env := newSessionEnv(t, stubLookup{}, "1")
	_, err := env.getSignatures(1)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Contains(t, err.Error(), "session not found")
}

func TestSessionHandler_GetSignaturesUsesHandshakePeer(t *testing.T) {
	var saw string
	env := newSessionEnv(t, stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}, sawAllow: &saw}}, "1")
	_, err := env.getSignatures(1)
	require.NoError(t, err)
	require.Equal(t, env.peer, saw, "allow must see the handshake peer, not a body field")
}

func TestSessionHandler_ResolvesEscrowOnce(t *testing.T) {
	n := 0
	env := newSessionEnv(t, countingLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}}, n: &n}, "1")
	_, err := env.getSignatures(1)
	require.NoError(t, err)
	require.Equal(t, 1, n, "roster and serve must share one SessionServerExisting call")
}

func TestAdaptLookup_NilServer(t *testing.T) {
	h := NewSessionHandler(AdaptLookup(func(string) (*transport.Server, error) {
		return nil, nil
	}))
	_, err := h.GetSignatures(
		WithEscrowID(withPeer(context.Background(), "gonka1peer"), "1"),
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}),
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Contains(t, err.Error(), "session not found")
}

func TestRequirePeer(t *testing.T) {
	_, _, err := requirePeer(context.Background())
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Contains(t, err.Error(), "handshake required")

	_, _, err = requirePeer(withPeer(context.Background(), "gonka1peer"))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "missing escrow id")

	peer, escrow, err := requirePeer(WithEscrowID(withPeer(context.Background(), "gonka1peer"), "escrow-1"))
	require.NoError(t, err)
	require.Equal(t, "gonka1peer", peer)
	require.Equal(t, "escrow-1", escrow)
}

func TestSessionHandler_GetSignaturesMissingEscrow(t *testing.T) {
	h := NewSessionHandler(stubLookup{core: stubCore{}})
	_, err := h.GetSignatures(withPeer(context.Background(), "gonka1peer"),
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "missing escrow id")
}

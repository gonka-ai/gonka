package rpcserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func requireHandshakeRequired(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Contains(t, err.Error(), "handshake required")
}

func TestSessionInterceptor_DropsEveryRPCExceptAttach(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(srv.Close)

	watch := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	stream, err := watch.Watch(context.Background(), connect.NewRequest(&rpcpb.WatchRequest{
		SessionToken: []byte("body-token-without-header"),
	}))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	requireHandshakeRequired(t, stream.Err())

	sigs := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err = sigs.GetSignatures(context.Background(), connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}))
	requireHandshakeRequired(t, err)

	gossip := rpcpbconnect.NewGossipServiceClient(srv.Client(), srv.URL)
	_, err = gossip.Nonce(context.Background(), connect.NewRequest(&rpcpb.SignedEnvelope{}))
	requireHandshakeRequired(t, err)

	payload := rpcpbconnect.NewPayloadServiceClient(srv.Client(), srv.URL)
	_, err = payload.GetPayload(context.Background(), connect.NewRequest(&rpcpb.GetPayloadRequest{}))
	requireHandshakeRequired(t, err)
}

func TestHandshakeGate_RejectsBeforeBody(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(srv.Close)

	body := bytes.Repeat([]byte("x"), 1<<20)
	req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.SessionServiceGetSignaturesProcedure, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSessionInterceptor_ForgedTokenDropped(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(srv.Close)
	_ = attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("forged-token-attach-0123456"))

	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err := client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}),
		[]byte("forged-session-token-xxxx"),
	))
	requireHandshakeRequired(t, err)
}

func TestSessionInterceptor_ExpiredTokenDropped(t *testing.T) {
	clock := &testClock{t: time.Unix(1_000_000, 0)}
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		SessionTTL: 30 * time.Second,
		Now:        clock.Now,
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{
		core: stubCore{sigs: map[uint32][]byte{0: {9}}},
	}))))
	t.Cleanup(srv.Close)
	attached := attachAt(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("expire-rpc-attach-nonce-01"), clock.Now().Unix())
	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)

	resp, err := client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
	require.NoError(t, err)
	require.Equal(t, []byte{9}, resp.Msg.Signatures[0])

	clock.Advance(31 * time.Second)
	_, err = client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
	requireHandshakeRequired(t, err)
}

func TestSessionInterceptor_HandshakeAdmitsThenUnimplemented(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("admit-unimpl-attach-012345"))

	gossip := rpcpbconnect.NewGossipServiceClient(srv.Client(), srv.URL)
	_, err := gossip.Nonce(context.Background(), withSession(connect.NewRequest(&rpcpb.SignedEnvelope{}), attached.SessionToken))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestSessionInterceptor_BindsPeerAndToken(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("bind-peer-attach-nonce-012"))

	header := make(http.Header)
	SetSessionHeader(header, attached.SessionToken)
	ctx, err := (&sessionInterceptor{auth: auth}).admit(
		context.Background(),
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
		header,
	)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), PeerFromContext(ctx))
	require.Equal(t, attached.SessionToken, TokenFromContext(ctx))
}

func TestAdmitSession_LookupUsesRawToken(t *testing.T) {
	nonce := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00}
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	_, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)

	header := make(http.Header)
	SetSessionHeader(header, nonce)
	ctx, err := admitSession(auth, context.Background(), header)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), PeerFromContext(ctx))
	require.Equal(t, nonce, TokenFromContext(ctx))

	auth.mu.RLock()
	_, hexKey := auth.sessions[hex.EncodeToString(nonce)]
	auth.mu.RUnlock()
	require.False(t, hexKey)
}

// handshakeGate admits before Connect reads the body; the interceptor must not
// repeat that work. LookupToken calls now() exactly once on a hit and nothing
// else on a GetSignatures request calls it, so the clock counts admissions.
func TestSessionInterceptor_AdmitsOncePerRPC(t *testing.T) {
	var nowCalls atomic.Int64
	auth := newTestAuth(PeerAuthConfig{Now: func() time.Time {
		nowCalls.Add(1)
		return time.Now()
	}})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("admit-once-attach-nonce-01"))

	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	nowCalls.Store(0)
	_, err := client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
	require.NoError(t, err)
	require.EqualValues(t, 1, nowCalls.Load(), "the gate and the interceptor must not both look the token up")
}

// The interceptor stays a complete gate on a mux built without handshakeGate.
func TestSessionInterceptor_AdmitsWithoutGate(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	attached, err := attachDirect(t, auth, signer, []byte("no-gate-attach-nonce-01234"))
	require.NoError(t, err)

	header := make(http.Header)
	SetSessionHeader(header, attached.SessionToken)
	ctx, err := (&sessionInterceptor{auth: auth}).admit(
		context.Background(),
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
		header,
	)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), PeerFromContext(ctx))

	_, err = (&sessionInterceptor{auth: auth}).admit(
		context.Background(),
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
		make(http.Header),
	)
	requireHandshakeRequired(t, err)
}

func TestHandshakeGate_RejectsOversizedAttachBeforeDecode(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)

	body := bytes.Repeat([]byte("x"), maxAttachRecvBytes+1)
	req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.PeerAuthServiceAttachProcedure, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	req.ContentLength = int64(len(body))
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestIsAttachPath_Exact(t *testing.T) {
	require.True(t, isAttachPath(rpcpbconnect.PeerAuthServiceAttachProcedure))
	require.False(t, isAttachPath("/decoy"+rpcpbconnect.PeerAuthServiceAttachProcedure))
	require.False(t, isAttachPath(rpcpbconnect.PeerAuthServiceWatchProcedure))
	require.False(t, isAttachPath("/"+rpcpbconnect.PeerAuthServiceAttachProcedure))
}

func TestIsWatchPath_Exact(t *testing.T) {
	require.True(t, isWatchPath(rpcpbconnect.PeerAuthServiceWatchProcedure))
	require.False(t, isWatchPath("/decoy"+rpcpbconnect.PeerAuthServiceWatchProcedure))
	require.False(t, isWatchPath(rpcpbconnect.PeerAuthServiceAttachProcedure))
}

func TestHandshakeGate_DecoyAttachPathRequiresHandshake(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)

	body := bytes.Repeat([]byte("x"), maxAttachRecvBytes+1)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/decoy"+rpcpbconnect.PeerAuthServiceAttachProcedure, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	req.ContentLength = int64(len(body))
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a suffixed decoy must not skip the handshake as Attach")
}

func TestSessionInterceptor_OversizedTokenDropped(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	header := make(http.Header)
	header.Set(SessionHeader, strings.Repeat("aa", maxAttachNonceBytes+1))
	_, err := (&sessionInterceptor{auth: auth}).admit(
		context.Background(),
		rpcpbconnect.SessionServiceGetSignaturesProcedure,
		header,
	)
	requireHandshakeRequired(t, err)
}

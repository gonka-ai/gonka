package rpcserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

func requireHandshakeRequired(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Contains(t, err.Error(), "handshake required")
}

func requireInvalidSessionToken(t *testing.T, err error) {
	t.Helper()
	requireHandshakeRequired(t, err)
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	require.Equal(t, transport.DevshardErrorInvalidSessionToken, ce.Meta().Get(transport.HeaderDevshardError))
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

	chat := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	chatStream, err := chat.Chat(context.Background(), connect.NewRequest(&rpcpb.SignedEnvelope{}))
	require.NoError(t, err)
	require.False(t, chatStream.Receive())
	requireHandshakeRequired(t, chatStream.Err())
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

	chat := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	stream, err := chat.Chat(context.Background(), withSession(connect.NewRequest(&rpcpb.SignedEnvelope{}), attached.SessionToken))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(stream.Err()))
}

func TestHandshakeGate_UnimplementedRejectsBeforeBody(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("unimpl-body-attach-nonce-01"))

	post := func(body []byte) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.GossipServiceNonceProcedure, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/proto")
		SetSessionHeader(req.Header, attached.SessionToken)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp.StatusCode
	}
	require.Equal(t, http.StatusNotImplemented, post(nil))
	require.Equal(t, http.StatusNotImplemented, post(bytes.Repeat([]byte{0}, maxRecvBytes+1)),
		"unimplemented must not reach the Connect read cap")
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
	require.Empty(t, TokenFromContext(ctx), "unary RPCs must not stash the token")

	ctx, err = (&sessionInterceptor{auth: auth}).admit(
		context.Background(),
		rpcpbconnect.PeerAuthServiceWatchProcedure,
		header,
	)
	require.NoError(t, err)
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
	ctx, err := admitSession(auth, context.Background(), header, false)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), PeerFromContext(ctx))
	require.Empty(t, TokenFromContext(ctx))

	ctx, err = admitSession(auth, context.Background(), header, true)
	require.NoError(t, err)
	require.Equal(t, nonce, TokenFromContext(ctx))

	auth.mu.RLock()
	_, hexKey := auth.sessions[hex.EncodeToString(nonce)]
	auth.mu.RUnlock()
	require.False(t, hexKey)
}

// handshakeGate admits before Connect reads the body; the interceptor must not
// repeat that work. LookupToken calls now() once on a hit; the channel limiter
// also reads Now for the token bucket; minute-bucket recording reads it once
// more. Nothing else on GetSignatures should.
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
	require.EqualValues(t, 3, nowCalls.Load(), "handshakeGate admits once, limiter + traffic share Now; the interceptor must not look the token up again")
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

func oversizedAttachRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	body := bytes.Repeat([]byte("x"), maxAttachRecvBytes+1)
	req, err := http.NewRequest(http.MethodPost, url+rpcpbconnect.PeerAuthServiceAttachProcedure, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	req.ContentLength = int64(len(body))
	return req
}

func requireHTTPMessage(t *testing.T, resp *http.Response, status int, msg string) {
	t.Helper()
	require.Equal(t, status, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), msg)
}

func TestHandshakeGate_RejectsOversizedAttachBeforeDecode(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Do(oversizedAttachRequest(t, srv.URL))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	requireHTTPMessage(t, resp, http.StatusTooManyRequests, "attach request too large")
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

func TestHandshakeGate_InvalidTokenSetsDevshardError(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)

	post := func(token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+rpcpbconnect.PeerAuthServiceWatchProcedure, nil)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/proto")
		if token != "" {
			req.Header.Set(SessionHeader, token)
		}
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	forged := post("not-a-token")
	require.Equal(t, http.StatusUnauthorized, forged.StatusCode)
	require.Equal(t, transport.DevshardErrorInvalidSessionToken, forged.Header.Get(transport.HeaderDevshardError))

	missing := post("")
	require.Equal(t, http.StatusUnauthorized, missing.StatusCode)
	require.Empty(t, missing.Header.Get(transport.HeaderDevshardError),
		"a missing token must not spend the versiond per-IP budget")
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
	requireInvalidSessionToken(t, err)
}

func TestAdmitSession_CountsGateReasons(t *testing.T) {
	clock := &testClock{t: time.Unix(1_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: 30 * time.Second, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	attached, err := attachDirectAt(t, auth, signer, []byte("gate-metric-attach-nonce01"), clock.Now().Unix())
	require.NoError(t, err)

	delta := func(reason string, fn func()) {
		t.Helper()
		before := metricCounter(t, "devshard_peer_rpc_gate_total", map[string]string{"reason": reason})
		fn()
		require.Equal(t, before+1, metricCounter(t, "devshard_peer_rpc_gate_total", map[string]string{"reason": reason}), reason)
	}

	delta(gateReasonMissing, func() {
		_, err := admitSession(auth, context.Background(), make(http.Header), false)
		requireHandshakeRequired(t, err)
	})

	delta(gateReasonOversized, func() {
		header := make(http.Header)
		header.Set(SessionHeader, strings.Repeat("aa", maxAttachNonceBytes+1))
		_, err := admitSession(auth, context.Background(), header, false)
		requireHandshakeRequired(t, err)
	})

	delta(gateReasonForged, func() {
		header := make(http.Header)
		SetSessionHeader(header, []byte("forged-session-token-xxxx"))
		_, err := admitSession(auth, context.Background(), header, false)
		requireHandshakeRequired(t, err)
	})

	delta(gateReasonAdmitted, func() {
		header := make(http.Header)
		SetSessionHeader(header, attached.SessionToken)
		ctx, err := admitSession(auth, context.Background(), header, false)
		require.NoError(t, err)
		require.Equal(t, signer.Address(), PeerFromContext(ctx))
		require.Empty(t, TokenFromContext(ctx))
	})

	clock.Advance(31 * time.Second)
	delta(gateReasonExpired, func() {
		header := make(http.Header)
		SetSessionHeader(header, attached.SessionToken)
		_, err := admitSession(auth, context.Background(), header, false)
		requireHandshakeRequired(t, err)
	})
}

func TestHandshakeGate_OversizedAttachCountsResourceExhausted(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)

	before := metricCounter(t, "devshard_peer_rpc_attach_total", map[string]string{"result": "resource_exhausted"})
	resp, err := srv.Client().Do(oversizedAttachRequest(t, srv.URL))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, before+1, metricCounter(t, "devshard_peer_rpc_attach_total", map[string]string{"result": "resource_exhausted"}))
}

func TestHandshakeGate_OversizedAttachConsumesFloor(t *testing.T) {
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 1})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Do(oversizedAttachRequest(t, srv.URL))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	requireHTTPMessage(t, resp, http.StatusTooManyRequests, "attach request too large")
	require.Equal(t, int32(0), spy.n.Load())

	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	req, err := signedAttach(testutil.MustGenerateKey(t), []byte("oversized-floor-nonce-aaaa"), time.Now().Unix())
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(req))
	requireResourceExhausted(t, err, "too many attach attempts")
	require.Equal(t, int32(0), spy.n.Load(), "floor charged on oversized must fire before ECDSA")
}

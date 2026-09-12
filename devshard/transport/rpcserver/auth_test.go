package rpcserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

const testHostAddress = "host-under-test"

// testEscrowID stands in for the escrow the Echo mount puts on the context.
const testEscrowID = "escrow-under-test"

func withTestEscrow(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), testEscrowID)))
	})
}

func newTestAuth(cfg PeerAuthConfig) *PeerAuthHandler {
	return NewPeerAuthHandler(signing.NewSecp256k1Verifier(), testHostAddress, cfg)
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func attach(t *testing.T, client rpcpbconnect.PeerAuthServiceClient, signer *signing.Secp256k1Signer, attachNonce []byte) *rpcpb.AttachResponse {
	t.Helper()
	return attachAt(t, client, signer, attachNonce, time.Now().Unix())
}

func attachAt(t *testing.T, client rpcpbconnect.PeerAuthServiceClient, signer *signing.Secp256k1Signer, attachNonce []byte, ts int64) *rpcpb.AttachResponse {
	t.Helper()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), attachNonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	resp, err := client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     attachNonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	return resp.Msg
}

func withSession[T any](req *connect.Request[T], token []byte) *connect.Request[T] {
	SetSessionHeader(req.Header(), token)
	return req
}

func TestPeerAuth_AttachWatch(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attachNonce := []byte("attach-nonce-bytes-0123456789")
	attached := attach(t, client, signer, attachNonce)
	require.Equal(t, attachNonce, attached.SessionToken)
	require.Equal(t, defaultMessagesPerMin, attached.Limits.GetMessagesPerMin())
	require.Equal(t, defaultMaxStreams, attached.Limits.GetMaxStreams())
	require.Equal(t, defaultAttachPerMin, attached.Limits.GetAttachPerMin())
	peer, ok := auth.LookupToken(attached.SessionToken)
	require.True(t, ok)
	require.Equal(t, signer.Address(), peer)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{SessionToken: attached.SessionToken}), attached.SessionToken))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())
	cancel()
	_ = stream.Close()
	for stream.Receive() {
	}
	_, ok = auth.LookupToken(attached.SessionToken)
	require.True(t, ok, "Watch termination must not drop the host session")

	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	again, err := client.Watch(ctx2, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), attached.SessionToken))
	require.NoError(t, err)
	require.True(t, again.Receive(), again.Err(), "a later Watch on the same token must be allowed")
}

func TestPeerAuth_AttachLiveNonceRejected(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attachNonce := []byte("replay-attach-nonce-0123456789")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), attachNonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	req := &rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     attachNonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}
	_, err = client.Attach(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(req))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestPeerAuth_AttachCountsOk(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	before := metricCounter(t, "devshard_peer_rpc_attach_total", map[string]string{"result": "ok"})
	_, err := attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("attach-ok-metric-nonce-012"))
	require.NoError(t, err)
	require.Equal(t, before+1, metricCounter(t, "devshard_peer_rpc_attach_total", map[string]string{"result": "ok"}))
}

func TestPeerAuth_AttachIdentity(t *testing.T) {
	realSigner := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	t.Run("claimed address mismatch", func(t *testing.T) {
		attachNonce := []byte("mismatch-attach-nonce-0123456")
		ts := time.Now().Unix()
		sig, err := transport.SignAttach(realSigner, testHostAddress, ts, other.Address(), attachNonce, transport.AttachProtocolVersion, nil)
		require.NoError(t, err)
		_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
			PeerAddress:     other.Address(),
			AttachNonce:     attachNonce,
			ProtocolVersion: transport.AttachProtocolVersion,
			HostAddress:     testHostAddress,
			Timestamp:       ts,
			Signature:       sig,
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	})

	t.Run("wrong host", func(t *testing.T) {
		attachNonce := []byte("wrong-host-attach-nonce-012345")
		ts := time.Now().Unix()
		sig, err := transport.SignAttach(other, "other-host", ts, other.Address(), attachNonce, "", nil)
		require.NoError(t, err)
		before := metricCounter(t, "devshard_peer_rpc_attach_total", map[string]string{"result": "unauthenticated"})
		_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
			PeerAddress: other.Address(),
			AttachNonce: attachNonce,
			HostAddress: "other-host",
			Timestamp:   ts,
			Signature:   sig,
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		require.Equal(t, before+1, metricCounter(t, "devshard_peer_rpc_attach_total", map[string]string{"result": "unauthenticated"}))
	})
}

func TestPeerAuth_TokenExpires(t *testing.T) {
	clock := &testClock{t: time.Unix(1_000_000, 0)}
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		SessionTTL: 30 * time.Second,
		Now:        clock.Now,
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attachNonce := []byte("ttl-attach-nonce-0123456789ab")
	attached := attachAt(t, client, signer, attachNonce, clock.Now().Unix())
	_, ok := auth.LookupToken(attached.SessionToken)
	require.True(t, ok)

	clock.Advance(31 * time.Second)
	_, ok = auth.LookupToken(attached.SessionToken)
	require.False(t, ok)
	require.Equal(t, 1, auth.SessionCount(), "LookupToken must leave expired entries for the sweeper")
	auth.SweepOnce()
	require.Equal(t, 0, auth.SessionCount())
}

func TestPeerAuth_AttachNonceBoundToPeer(t *testing.T) {
	first := testutil.MustGenerateKey(t)
	second := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	shared := []byte("shared-attach-nonce-012345678")
	attach(t, client, first, shared)
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(second, testHostAddress, ts, second.Address(), shared, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     second.Address(),
		AttachNonce:     shared,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestPeerAuth_ReattachReplacesSession(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attach(t, client, signer, []byte("replace-attach-nonce-aaaaaaa"))
	second := attach(t, client, signer, []byte("replace-attach-nonce-bbbbbbb"))
	require.False(t, bytes.Equal(first.SessionToken, second.SessionToken))
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "replaced token stays valid for TokenGrace so in-flight RPCs still admit")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_ReattachGraceExpires(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)

	first, err := attachDirect(t, auth, signer, []byte("grace-attach-nonce-aaaaaaaa"))
	require.NoError(t, err)
	second, err := attachDirect(t, auth, signer, []byte("grace-attach-nonce-bbbbbbbb"))
	require.NoError(t, err)

	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
	require.Equal(t, 2, auth.SessionCount())

	clock.Advance(defaultTokenGrace + time.Second)
	_, ok = auth.LookupToken(first.SessionToken)
	require.False(t, ok)
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
	require.Equal(t, 2, auth.SessionCount(), "LookupToken leaves expired grace for the sweeper")
	auth.SweepOnce()
	require.Equal(t, 1, auth.SessionCount())
}

func TestPeerAuth_ReplayDoesNotEvictNewerSession(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonceA := []byte("replay-evict-nonce-aaaaaaaa")
	nonceB := []byte("replay-evict-nonce-bbbbbbbb")

	reqA, err := signedAttach(signer, nonceA, clock.Now().Unix())
	require.NoError(t, err)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(reqA))
	require.NoError(t, err)

	clock.Advance(time.Second)
	second, err := attachDirect(t, auth, signer, nonceB)
	require.NoError(t, err)

	clock.Advance(defaultTokenGrace + time.Second)
	auth.SweepOnce()
	_, ok := auth.LookupToken(nonceA)
	require.False(t, ok)

	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(reqA))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "attach_nonce already in use")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok, "replay of A must not evict B")
}

func TestPeerAuth_StaleAttachRejected(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	_, err := attachDirect(t, auth, signer, []byte("stale-attach-nonce-aaaaaaaa"))
	require.NoError(t, err)
	staleTS := clock.Now().Unix()

	clock.Advance(2 * time.Second)
	live, err := attachDirect(t, auth, signer, []byte("stale-attach-nonce-bbbbbbbb"))
	require.NoError(t, err)

	req, err := signedAttach(signer, []byte("stale-attach-nonce-cccccccc"), staleTS)
	require.NoError(t, err)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "attach is not newer than the live session")
	_, ok := auth.LookupToken(live.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_ExpiredNonceRebindAfterRetiredTTL(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: 30 * time.Second, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonce := []byte("expired-rebind-nonce-aaaaaa")
	_, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)

	clock.Advance(31 * time.Second)
	auth.SweepOnce()
	_, err = attachDirect(t, auth, signer, nonce)
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "attach_nonce already in use")

	clock.Advance(retiredNonceTTL + time.Second)
	_, err = attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)
}

func TestPeerAuth_RetiredNonceCoversFutureSkew(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	clock := &testClock{t: base}
	auth := newTestAuth(PeerAuthConfig{Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonce := []byte("skew-retire-nonce-aaaaaaaa")
	futureTS := base.Unix() + transport.MaxTimestampDrift
	req, err := signedAttach(signer, nonce, futureTS)
	require.NoError(t, err)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.NoError(t, err)

	auth.InvalidateToken(nonce)
	clock.Advance(time.Duration(transport.MaxTimestampDrift)*time.Second + time.Second)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a future-skewed Attach must stay retired while its signature still verifies")
	require.Contains(t, err.Error(), "attach_nonce already in use")
	_, ok := auth.LookupToken(nonce)
	require.False(t, ok)
}

func TestPeerAuth_OnlyOneGraceToken(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)

	a, err := attachDirect(t, auth, signer, []byte("one-grace-nonce-aaaaaaaaaaa"))
	require.NoError(t, err)
	b, err := attachDirect(t, auth, signer, []byte("one-grace-nonce-bbbbbbbbbbb"))
	require.NoError(t, err)
	c, err := attachDirect(t, auth, signer, []byte("one-grace-nonce-ccccccccccc"))
	require.NoError(t, err)

	_, ok := auth.LookupToken(a.SessionToken)
	require.False(t, ok, "a third Attach must drop the older grace token")
	_, ok = auth.LookupToken(b.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(c.SessionToken)
	require.True(t, ok)
	require.Equal(t, 2, auth.SessionCount())
}

func TestPeerAuth_GraceAdmitsOldHeader(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	mux := NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {7}}}}))
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	authClient := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attachAt(t, authClient, signer, []byte("grace-rpc-nonce-aaaaaaaaaaa"), clock.Now().Unix())
	second := attachAt(t, authClient, signer, []byte("grace-rpc-nonce-bbbbbbbbbbb"), clock.Now().Unix())
	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)

	resp, err := client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), first.SessionToken))
	require.NoError(t, err, "GetSignatures with the old header must succeed during grace")
	require.Equal(t, []byte{7}, resp.Msg.Signatures[0])

	resp, err = client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), second.SessionToken))
	require.NoError(t, err)

	clock.Advance(defaultTokenGrace + time.Second)
	_, err = client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), first.SessionToken))
	requireHandshakeRequired(t, err)
	_, err = client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), second.SessionToken))
	require.NoError(t, err)
}

func TestPeerAuth_RejectsNonEmptyChannelBinding(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attachNonce := []byte("binding-attach-nonce-0123456")
	binding := []byte("not-a-tls-fingerprint")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), attachNonce, transport.AttachProtocolVersion, binding)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     attachNonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		ChannelBinding:  binding,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestPeerAuth_RejectsShortAttachNonce(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), []byte("short"), "", nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress: signer.Address(),
		AttachNonce: []byte("short"),
		HostAddress: testHostAddress,
		Timestamp:   ts,
		Signature:   sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestPeerAuth_RejectsLongAttachNonce(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := bytes.Repeat([]byte("n"), maxAttachNonceBytes+1)
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "attach_nonce")
}

func TestPeerAuth_RejectsEmptyProtocolVersion(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := []byte("empty-proto-attach-nonce-0123")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, "", nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress: signer.Address(),
		AttachNonce: nonce,
		HostAddress: testHostAddress,
		Timestamp:   ts,
		Signature:   sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "protocol_version is required")
}

func TestPeerAuth_RejectsUnknownProtocolVersion(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := []byte("unknown-proto-attach-nonce-01")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, "v5", nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: "v5",
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "unsupported protocol_version")
	require.NotContains(t, err.Error(), "v5")
}

func TestPeerAuth_WatchRejectsMissingToken(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	stream, err := client.Watch(context.Background(), connect.NewRequest(&rpcpb.WatchRequest{}))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(stream.Err()))
}

func TestPeerAuth_DropsRPCWithoutHandshake(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	session := NewSessionHandler(stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}}})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, session)))
	t.Cleanup(srv.Close)

	watch := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	stream, err := watch.Watch(context.Background(), connect.NewRequest(&rpcpb.WatchRequest{SessionToken: []byte("not-a-session-token!!")}))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(stream.Err()),
		"Watch without Attach must not reach the handler")

	sigs := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err = sigs.GetSignatures(context.Background(), connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"GetSignatures without Attach must be dropped")
}

func TestPeerAuth_TokenWorksOnOtherEscrow(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var attachEscrow string
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(ctx context.Context, addr string) (bool, error) {
			attachEscrow = EscrowIDFromContext(ctx)
			return addr == signer.Address(), nil
		},
	})
	mux := NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {1}}}}))
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), "other-escrow")))
	}))
	t.Cleanup(other.Close)

	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer, []byte("host-scope-attach-nonce-0123"))
	require.Equal(t, testEscrowID, attachEscrow)
	_, ok := auth.LookupToken(attached.SessionToken)
	require.True(t, ok)

	client := rpcpbconnect.NewSessionServiceClient(other.Client(), other.URL)
	resp, err := client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
	require.NoError(t, err)
	require.Equal(t, []byte{1}, resp.Msg.Signatures[0])
}

func TestPeerAuth_LiveRenewalSkipsDoor(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var doorCalls atomic.Int32
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(ctx context.Context, addr string) (bool, error) {
			doorCalls.Add(1)
			if EscrowIDFromContext(ctx) == transport.HostRPCEscrowID {
				return false, storage.ErrSessionNotFound
			}
			return addr == signer.Address(), nil
		},
	})
	mux := NewMux(auth, nil)
	door := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(door.Close)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), transport.HostRPCEscrowID)))
	}))
	t.Cleanup(host.Close)

	first := attach(t, rpcpbconnect.NewPeerAuthServiceClient(door.Client(), door.URL), signer,
		[]byte("live-renew-door-nonce-012345"))
	require.Equal(t, int32(1), doorCalls.Load())
	second := attach(t, rpcpbconnect.NewPeerAuthServiceClient(host.Client(), host.URL), signer,
		[]byte("live-renew-host-nonce-012345"))
	require.Equal(t, int32(1), doorCalls.Load(), "live renewal must not re-run AllowsSender")
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "replaced token stays valid for TokenGrace")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_WatchOnHostPath(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	mux := NewMux(auth, nil)
	door := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(door.Close)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), transport.HostRPCEscrowID)))
	}))
	t.Cleanup(host.Close)

	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(door.Client(), door.URL), signer,
		[]byte("watch-host-path-nonce-0123456"))
	client := rpcpbconnect.NewPeerAuthServiceClient(host.Client(), host.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), attached.SessionToken))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err(), "Watch on /sessions/_/rpc must admit a live token")
}

func TestPeerAuth_SecondAttachReplacesOnAnyEscrowPath(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	mux := NewMux(auth, nil)
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), "second-escrow")))
	}))
	t.Cleanup(otherSrv.Close)

	first := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL), signer,
		[]byte("per-host-attach-nonce-aaaaaa"))
	second := attach(t, rpcpbconnect.NewPeerAuthServiceClient(otherSrv.Client(), otherSrv.URL), signer,
		[]byte("per-host-attach-nonce-bbbbbb"))

	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "replaced token stays valid for TokenGrace")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_WatchIgnoresBodyToken(t *testing.T) {
	a := testutil.MustGenerateKey(t)
	b := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	tokenA := attach(t, client, a, []byte("watch-body-token-aaaa-012345")).SessionToken
	tokenB := attach(t, client, b, []byte("watch-body-token-bbbb-012345")).SessionToken

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{SessionToken: tokenA}), tokenB))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())
	cancel()
	_ = stream.Close()
	for stream.Receive() {
	}
	_, ok := auth.LookupToken(tokenB)
	require.True(t, ok, "Watch termination must not drop the header token")
	_, ok = auth.LookupToken(tokenA)
	require.True(t, ok, "Watch must not invalidate a different peer's token from the body")
}

func TestPeerAuth_AttachWithoutEscrow(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(NewMux(auth, nil))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attached := attach(t, client, signer, []byte("no-escrow-attach-nonce-01234"))
	_, ok := auth.LookupToken(attached.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_AttachRequiresParticipant(t *testing.T) {
	member := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(_ context.Context, addr string) (bool, error) {
			return addr == member.Address(), nil
		},
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attached := attach(t, client, member, []byte("allow-member-attach-nonce-01"))
	_, ok := auth.LookupToken(attached.SessionToken)
	require.True(t, ok)

	ts := time.Now().Unix()
	nonce := []byte("allow-outsider-attach-nonce-0")
	sig, err := transport.SignAttach(other, testHostAddress, ts, other.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     other.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "peer is not a known participant")
	_, ok = auth.LookupToken(nonce)
	require.False(t, ok)
}

func TestPeerAuth_AttachAllowRequiresEscrow(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(context.Context, string) (bool, error) { return true, nil },
	})
	srv := httptest.NewServer(NewMux(auth, nil))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	ts := time.Now().Unix()
	nonce := []byte("allow-missing-escrow-nonce-01")
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "missing escrow id")
	_, ok := auth.LookupToken(nonce)
	require.False(t, ok)
}

func TestPeerAuth_AttachEscrowNotOpen(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(context.Context, string) (bool, error) {
			return false, errors.New("storage: not found")
		},
	})
	_, err := attachDirect(t, auth, signer, []byte("allow-not-open-attach-nonce"))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow is not open on this host")
	require.NotContains(t, err.Error(), "storage")
}

func TestPeerAuth_AttachChainUnavailable(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(context.Context, string) (bool, error) {
			return false, fmt.Errorf("get escrow: %w", bridge.ErrChainUnavailable)
		},
	})
	_, err := attachDirect(t, auth, signer, []byte("allow-chain-unavail-nonce-a"))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, err.Error(), "chain unavailable")
	require.NotContains(t, err.Error(), "get escrow")
}

func TestPeerAuth_AttachInitializing(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(context.Context, string) (bool, error) {
			return false, fmt.Errorf("escrow %s is not open on this host: %w", testEscrowID, storage.ErrStorageIndexRebuilding)
		},
	})
	_, err := attachDirect(t, auth, signer, []byte("allow-rebuilding-attach-non"))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host initializing")
	require.NotContains(t, err.Error(), "rebuilding")
}

func TestPeerAuth_SecondWatchRejected(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	token := attach(t, client, signer, []byte("one-watch-attach-nonce-01234")).SessionToken

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.True(t, first.Receive(), first.Err())

	second, err := client.Watch(context.Background(), withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.False(t, second.Receive())
	require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(second.Err()))
	require.Contains(t, second.Err().Error(), "watch already active")

	_, ok := auth.LookupToken(token)
	require.True(t, ok, "rejected second Watch must not drop the session")

	cancel()
	_ = first.Close()
	for first.Receive() {
	}
	_, ok = auth.LookupToken(token)
	require.True(t, ok, "Watch termination must not drop the host session")

	again, err := client.Watch(context.Background(), withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.True(t, again.Receive(), again.Err(), "after the first Watch ends, a new Watch on the same token must be allowed")
	_ = again.Close()
}

func TestPeerAuth_OldWatchEndDoesNotDropReattachedSession(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attach(t, client, signer, []byte("old-watch-attach-nonce-aaaa"))
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), first.SessionToken))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	second := attach(t, client, signer, []byte("old-watch-attach-nonce-bbbb"))
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "old token remains for TokenGrace")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)

	cancel()
	_ = stream.Close()
	for stream.Receive() {
	}
	require.Eventually(t, func() bool {
		_, ok := auth.LookupToken(second.SessionToken)
		return ok
	}, time.Second, 10*time.Millisecond, "old Watch exit must not drop the re-attached session")
}

func TestPeerAuth_CloseEndsWatchAndStopsAdmitting(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: time.Hour})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attached := attach(t, client, signer, []byte("close-watch-attach-nonce-aa"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), attached.SessionToken))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	auth.Close()
	require.True(t, auth.Closed())
	require.False(t, stream.Receive(), "Close must end Watch, not wait for Heartbeat")
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(stream.Err()))
	require.Contains(t, stream.Err().Error(), "host shutting down")
	_, ok := auth.LookupToken(attached.SessionToken)
	require.False(t, ok)

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("close-attach-nonce-bbbbbbbb"))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host shutting down")

	session := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)
	_, err = session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), attached.SessionToken))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "host shutting down")
}

func TestPeerAuth_WatchEndsOnReattach(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: time.Hour})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attach(t, client, signer, []byte("watch-replace-nonce-aaaaaaa"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), first.SessionToken))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	second := attach(t, client, signer, []byte("watch-replace-nonce-bbbbbbb"))
	require.False(t, stream.Receive(), "Watch must end when its token is no longer current, not at the next heartbeat")
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(stream.Err()))
	require.Contains(t, stream.Err().Error(), "session replaced")
	_, ok := auth.LookupToken(second.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(first.SessionToken)
	require.True(t, ok, "grace token still admits unaries")
}

func TestPeerAuth_WatchEndsWhenSwept(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{Heartbeat: time.Hour, SessionTTL: 30 * time.Second, Now: clock.Now})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	signer := testutil.MustGenerateKey(t)
	token := attachAt(t, client, signer, []byte("watch-sweep-nonce-aaaaaaaa"), clock.Now().Unix()).SessionToken

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	clock.Advance(31 * time.Second)
	auth.SweepOnce()
	require.False(t, stream.Receive(), "sweep must end Watch, not wait for Heartbeat")
}

type writeDeadlineSpy struct {
	http.ResponseWriter
	mu    sync.Mutex
	setAt []time.Time
}

func (w *writeDeadlineSpy) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.setAt = append(w.setAt, t)
	return nil
}

func (w *writeDeadlineSpy) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func TestPeerAuth_WatchSetsWriteDeadline(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{WatchWriteTimeout: 50 * time.Millisecond})
	var spy *writeDeadlineSpy
	inner := withTestEscrow(NewMux(auth, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &writeDeadlineSpy{ResponseWriter: w}
		spy = s
		inner.ServeHTTP(s, r)
	}))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	token := attach(t, client, signer, []byte("watch-deadline-attach-012345")).SessionToken

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())
	cancel()
	_ = stream.Close()
	for stream.Receive() {
	}

	require.NotNil(t, spy)
	spy.mu.Lock()
	n := len(spy.setAt)
	spy.mu.Unlock()
	require.GreaterOrEqual(t, n, 1, "Watch must set a write deadline before Send")
}

type stallingWriter struct {
	http.ResponseWriter
	mu       sync.Mutex
	deadline time.Time
}

func (w *stallingWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

func (w *stallingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *stallingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	dl := w.deadline
	w.mu.Unlock()
	if dl.IsZero() {
		return w.ResponseWriter.Write(p)
	}
	if wait := time.Until(dl); wait > 0 {
		time.Sleep(wait)
	}
	return 0, os.ErrDeadlineExceeded
}

func TestPeerAuth_WatchWriteDeadlineEndsStream(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{WatchWriteTimeout: 40 * time.Millisecond, Heartbeat: time.Second})
	inner := withTestEscrow(NewMux(auth, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(&stallingWriter{ResponseWriter: w}, r)
	}))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	token := attach(t, client, signer, []byte("watch-stall-attach-nonce-012")).SessionToken

	stream, err := client.Watch(context.Background(), withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.Error(t, stream.Err())
	_, ok := auth.LookupToken(token)
	require.True(t, ok, "deadline exceeded Send must end the Watch stream, not the host session")
}

func attachDirect(t *testing.T, auth *PeerAuthHandler, signer *signing.Secp256k1Signer, nonce []byte) (*rpcpb.AttachResponse, error) {
	t.Helper()
	return attachDirectAt(t, auth, signer, nonce, auth.now().Unix())
}

func attachDirectAt(t *testing.T, auth *PeerAuthHandler, signer *signing.Secp256k1Signer, nonce []byte, ts int64) (*rpcpb.AttachResponse, error) {
	t.Helper()
	req, err := signedAttach(signer, nonce, ts)
	if err != nil {
		return nil, err
	}
	resp, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func signedAttach(signer *signing.Secp256k1Signer, nonce []byte, ts int64) (*rpcpb.AttachRequest, error) {
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	if err != nil {
		return nil, err
	}
	return &rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}, nil
}

func TestPeerAuth_MaxSessions(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{MaxSessions: 1})
	first := testutil.MustGenerateKey(t)
	second := testutil.MustGenerateKey(t)

	held, err := attachDirect(t, auth, first, []byte("max-sessions-nonce-aaaaaaa"))
	require.NoError(t, err)
	require.Equal(t, 1, auth.SessionCount())

	incoming, err := attachDirect(t, auth, second, []byte("max-sessions-nonce-bbbbbbb"))
	require.NoError(t, err, "a new peer must evict the oldest idle session, not resource_exhausted")
	_, ok := auth.LookupToken(held.SessionToken)
	require.False(t, ok)
	_, ok = auth.LookupToken(incoming.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_SamePeerReplaceAtCap(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{MaxSessions: 1})
	first := testutil.MustGenerateKey(t)
	second := testutil.MustGenerateKey(t)

	_, err := attachDirect(t, auth, first, []byte("cap-replace-nonce-aaaaaaaa"))
	require.NoError(t, err)
	id, _, err := auth.beginWatch([]byte("cap-replace-nonce-aaaaaaaa"))
	require.NoError(t, err)
	require.NotZero(t, id)

	_, err = attachDirect(t, auth, second, []byte("cap-replace-nonce-bbbbbbbb"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "a watching session is not idle")
	require.Contains(t, err.Error(), "too many sessions")

	replaced, err := attachDirect(t, auth, first, []byte("cap-replace-nonce-cccccccc"))
	require.NoError(t, err, "same peer must replace at the cap")
	_, ok := auth.LookupToken(replaced.SessionToken)
	require.True(t, ok)
}

// A full map is not a live map: LookupToken leaves expired entries for the
// sweeper, so Attach must sweep before it refuses.
func TestPeerAuth_MaxSessionsSweepsExpiredBeforeRefusing(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{MaxSessions: 2, SessionTTL: 30 * time.Second, Now: clock.Now})

	_, err := attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("cap-sweep-nonce-aaaaaaaaaa"))
	require.NoError(t, err)
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("cap-sweep-nonce-bbbbbbbbbb"))
	require.NoError(t, err)
	require.Equal(t, 2, auth.SessionCount())

	clock.Advance(31 * time.Second)
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("cap-sweep-nonce-cccccccccc"))
	require.NoError(t, err, "a map full of expired sessions must not refuse a new peer")
	require.Equal(t, 1, auth.SessionCount(), "the expired entries must be gone, not just stepped over")
}

func TestPeerAuth_MaxSessionsRefusesWhenAllWatching(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{MaxSessions: 2, SessionTTL: 30 * time.Second, Now: clock.Now})

	a := testutil.MustGenerateKey(t)
	b := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, a, []byte("cap-watch-nonce-aaaaaaaaaaa"))
	require.NoError(t, err)
	second, err := attachDirect(t, auth, b, []byte("cap-watch-nonce-bbbbbbbbbbb"))
	require.NoError(t, err)
	_, _, err = auth.beginWatch(first.SessionToken)
	require.NoError(t, err)
	_, _, err = auth.beginWatch(second.SessionToken)
	require.NoError(t, err)

	clock.Advance(10 * time.Second)
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("cap-watch-nonce-ccccccccccc"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_EvictsOldestIdleFirst(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{MaxSessions: 2, SessionTTL: time.Minute, Now: clock.Now})

	a := testutil.MustGenerateKey(t)
	b := testutil.MustGenerateKey(t)
	c := testutil.MustGenerateKey(t)
	older, err := attachDirect(t, auth, a, []byte("evict-idle-nonce-aaaaaaaaaa"))
	require.NoError(t, err)
	clock.Advance(time.Second)
	newer, err := attachDirect(t, auth, b, []byte("evict-idle-nonce-bbbbbbbbbb"))
	require.NoError(t, err)
	_, _, err = auth.beginWatch(newer.SessionToken)
	require.NoError(t, err)

	clock.Advance(time.Second)
	incoming, err := attachDirect(t, auth, c, []byte("evict-idle-nonce-cccccccccc"))
	require.NoError(t, err)
	_, ok := auth.LookupToken(older.SessionToken)
	require.False(t, ok, "the idle session must be the one evicted")
	_, ok = auth.LookupToken(newer.SessionToken)
	require.True(t, ok, "the watching session must stay")
	_, ok = auth.LookupToken(incoming.SessionToken)
	require.True(t, ok)
}

type countingVerifier struct {
	inner signing.Verifier
	n     atomic.Int32
}

func (v *countingVerifier) RecoverAddress(message, signature []byte) (string, error) {
	v.n.Add(1)
	return v.inner.RecoverAddress(message, signature)
}

func requireRetryAfter(t *testing.T, err error) {
	t.Helper()
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	got := ce.Meta().Get("Retry-After")
	require.NotEmpty(t, got, "resource_exhausted Attach must advertise Retry-After")
	sec, convErr := strconv.Atoi(got)
	require.NoError(t, convErr)
	require.GreaterOrEqual(t, sec, 1)
}

func TestPeerAuth_AttachFloorBeforeVerify(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})

	_, err := attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-attach-nonce-aaaaaaaaaa"))
	require.NoError(t, err)
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-attach-nonce-bbbbbbbbbb"))
	require.NoError(t, err)
	calls := spy.n.Load()

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-attach-nonce-cccccccccc"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "too many attach attempts")
	require.Equal(t, calls, spy.n.Load(), "floor must fire before ECDSA")
	requireRetryAfter(t, err)

	clock.Advance(time.Minute + time.Second)
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-attach-nonce-dddddddddd"))
	require.NoError(t, err)
}

func TestPeerAuth_FailedKnownPeerAttachConsumesFloor(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonce := []byte("failed-known-attach-nonce-aa")
	_, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)

	req, err := signedAttach(signer, nonce, clock.Now().Unix())
	require.NoError(t, err)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("failed-known-attach-nonce-bb"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"a failed Attach from a live peer must keep its floor charge")
	requireRetryAfter(t, err)
	require.Greater(t, spy.n.Load(), int32(0))
}

func TestPeerAuth_KnownPeerReattachDoesNotConsumeFloor(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})
	a := testutil.MustGenerateKey(t)
	for i := 0; i < 5; i++ {
		_, err := attachDirectAt(t, auth, a, []byte(fmt.Sprintf("known-reattach-%08dxxxx", i)), clock.Now().Unix()+int64(i))
		require.NoError(t, err)
	}
	_, err := attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-new-peer-nonce-bbbbbb"))
	require.NoError(t, err, "known-peer renewals must not occupy extra floor slots")

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-new-peer-nonce-cccccc"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	requireRetryAfter(t, err)
}

func TestPeerAuth_SweeperDropsExpired(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{
		SessionTTL:    30 * time.Second,
		SweepInterval: 15 * time.Millisecond,
		Now:           clock.Now,
	})
	t.Cleanup(auth.Close)
	signer := testutil.MustGenerateKey(t)
	_, err := attachDirect(t, auth, signer, []byte("sweep-attach-nonce-01234567"))
	require.NoError(t, err)
	require.Equal(t, 1, auth.SessionCount())

	clock.Advance(31 * time.Second)
	require.Equal(t, 1, auth.SessionCount())
	auth.StartSweeper()
	require.Eventually(t, func() bool {
		return auth.SessionCount() == 0
	}, time.Second, 5*time.Millisecond)
}

func TestPeerAuth_SweepOnceClearsMoreThanOneBatch(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	const n = sweepBatchSize + 2
	auth := newTestAuth(PeerAuthConfig{
		SessionTTL:        30 * time.Second,
		AttachFloorPerMin: n + 1,
		MaxSessions:       n,
		Now:               clock.Now,
	})
	for i := 0; i < n; i++ {
		nonce := []byte(fmt.Sprintf("swp-batch-%016d", i))
		_, err := attachDirect(t, auth, testutil.MustGenerateKey(t), nonce)
		require.NoError(t, err)
	}
	require.Equal(t, n, auth.SessionCount())
	clock.Advance(31 * time.Second)
	auth.SweepOnce()
	require.Equal(t, 0, auth.SessionCount())
}

func TestPeerAuth_LookupTokenConcurrent(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	signer := testutil.MustGenerateKey(t)
	attached, err := attachDirect(t, auth, signer, []byte("rwlock-attach-nonce-0123456"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 64; j++ {
				_, _ = auth.LookupToken(attached.SessionToken)
			}
		}()
	}
	wg.Wait()
	peer, ok := auth.LookupToken(attached.SessionToken)
	require.True(t, ok)
	require.Equal(t, signer.Address(), peer)
}

func TestPeerAuth_MapsUseRawTokenKeys(t *testing.T) {
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	auth := newTestAuth(PeerAuthConfig{})
	signer := testutil.MustGenerateKey(t)
	attached, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)
	require.Equal(t, nonce, attached.SessionToken)

	auth.mu.RLock()
	_, hasRaw := auth.sessions[string(nonce)]
	_, hasHex := auth.sessions[hex.EncodeToString(nonce)]
	peerTok := auth.byPeer[signer.Address()]
	auth.mu.RUnlock()
	require.True(t, hasRaw, "session map must be keyed by raw attach_nonce")
	require.False(t, hasHex, "session map must not be keyed by hex(token)")
	require.Equal(t, string(nonce), peerTok)
	peer, ok := auth.LookupToken(nonce)
	require.True(t, ok)
	require.Equal(t, signer.Address(), peer)
}

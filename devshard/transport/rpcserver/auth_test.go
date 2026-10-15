package rpcserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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

// watchAfterRelease opens a Watch once the previous ones have left their
// slots. Cancelling the client context makes Receive return before the
// server releases the per-peer Watch slot, so the next Watch can still be
// "too many concurrent watches".
func watchAfterRelease(t *testing.T, client rpcpbconnect.PeerAuthServiceClient, token []byte) {
	t.Helper()
	require.Eventually(t, func() bool {
		stream, err := client.Watch(context.Background(), withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
		if err != nil {
			if connect.CodeOf(err) == connect.CodeResourceExhausted {
				return false
			}
			t.Fatalf("reopened Watch: %v", err)
		}
		if stream.Receive() {
			t.Cleanup(func() { _ = stream.Close() })
			return true
		}
		recvErr := stream.Err()
		_ = stream.Close()
		if connect.CodeOf(recvErr) == connect.CodeResourceExhausted {
			return false
		}
		t.Fatalf("reopened Watch: %v", recvErr)
		return false
	}, time.Second, 5*time.Millisecond, "after the first Watch ends, a new Watch on the same token must be allowed")
}

func TestPeerAuth_AttachWatch(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attachNonce := []byte("attach-nonce-bytes-0123456789")
	attached := attach(t, client, signer, attachNonce)
	require.NotEqual(t, attachNonce, attached.SessionToken)
	require.Equal(t, defaultMessagesPerMin, attached.Limits.GetMessagesPerMin())
	require.Equal(t, defaultMessagesPerMin/10, attached.Limits.GetMessagesBurst())
	require.Equal(t, defaultMaxStreams, attached.Limits.GetMaxStreams())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpWeightPerMin())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpBurst())
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

	watchAfterRelease(t, client, attached.SessionToken)
}

func TestPeerAuth_AttachReplayOverHTTPReturnsSameToken(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	attachNonce := []byte("replay-attach-nonce-0123456789")
	ts := time.Now().Unix() - 1
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
	first, err := client.Attach(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	second, err := client.Attach(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	require.Equal(t, first.Msg.SessionToken, second.Msg.SessionToken)
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

	clock.Advance(30*time.Second + sessionTokenSkew)
	_, ok = auth.LookupToken(attached.SessionToken)
	require.True(t, ok, "a token admits through the replica skew")

	clock.Advance(time.Second)
	_, ok = auth.LookupToken(attached.SessionToken)
	require.False(t, ok)
}

func TestPeerAuth_AttachNonceBoundToPeer(t *testing.T) {
	first := testutil.MustGenerateKey(t)
	second := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	shared := []byte("shared-attach-nonce-012345678")
	firstTok := attach(t, client, first, shared)
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(second, testHostAddress, ts, second.Address(), shared, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	resp, err := client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     second.Address(),
		AttachNonce:     shared,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
	require.NotEqual(t, firstTok.SessionToken, resp.Msg.SessionToken)
	peer, ok := auth.LookupToken(firstTok.SessionToken)
	require.True(t, ok)
	require.Equal(t, first.Address(), peer)
	peer, ok = auth.LookupToken(resp.Msg.SessionToken)
	require.True(t, ok)
	require.Equal(t, second.Address(), peer)
}

func TestPeerAuth_ReattachAddsToken(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attach(t, client, signer, []byte("replace-attach-nonce-aaaaaaa"))
	second := attach(t, client, signer, []byte("replace-attach-nonce-bbbbbbb"))
	require.False(t, bytes.Equal(first.SessionToken, second.SessionToken))
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "the earlier token stays valid so in-flight RPCs still admit")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_LaterAttachDoesNotShortenEarlierToken(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)

	first, err := attachDirect(t, auth, signer, []byte("later-attach-nonce-aaaaaaaa"))
	require.NoError(t, err)
	second, err := attachDirect(t, auth, signer, []byte("later-attach-nonce-bbbbbbbb"))
	require.NoError(t, err)

	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)

	clock.Advance(10 * time.Second)
	_, ok = auth.LookupToken(first.SessionToken)
	require.True(t, ok, "a later Attach does not shorten the earlier token")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_ReplayLeavesNewerTokenLive(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonceA := []byte("replay-evict-nonce-aaaaaaaa")
	nonceB := []byte("replay-evict-nonce-bbbbbbbb")

	reqA, err := signedAttach(signer, nonceA, clock.Now().Unix())
	require.NoError(t, err)
	first, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(reqA))
	require.NoError(t, err)

	clock.Advance(time.Second)
	second, err := attachDirect(t, auth, signer, nonceB)
	require.NoError(t, err)

	replay, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(reqA))
	require.NoError(t, err)
	require.Equal(t, first.Msg.SessionToken, replay.Msg.SessionToken)
	_, ok := auth.LookupToken(second.SessionToken)
	require.True(t, ok, "replay of A leaves B live")
}

func TestPeerAuth_OlderTimestampAttachStillIssues(t *testing.T) {
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
	older, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.NoError(t, err)
	_, ok := auth.LookupToken(live.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(older.Msg.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_NonceReuseAfterExpiryIssues(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: 30 * time.Second, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonce := []byte("expired-rebind-nonce-aaaaaa")
	_, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)

	clock.Advance(31 * time.Second)
	again, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)
	_, ok := auth.LookupToken(again.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_FutureTimestampDoesNotExtendExpiry(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	clock := &testClock{t: base}
	const ttl = 2 * time.Minute
	auth := newTestAuth(PeerAuthConfig{SessionTTL: ttl, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	futureTS := base.Unix() + transport.MaxTimestampDrift
	req, err := signedAttach(signer, []byte("future-ts-attach-nonce-aaaa"), futureTS)
	require.NoError(t, err)
	resp, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.NoError(t, err, "a timestamp inside the drift window is accepted")
	require.Equal(t, base.Add(ttl).Unix(), resp.Msg.ExpiresAt, "expiry is anchored to the host clock")

	clock.Advance(ttl + sessionTokenSkew)
	_, ok := auth.LookupToken(resp.Msg.SessionToken)
	require.True(t, ok)
	clock.Advance(time.Second)
	_, ok = auth.LookupToken(resp.Msg.SessionToken)
	require.False(t, ok, "worst-case lifetime is TTL plus the replica skew")
}

func TestPeerAuth_EveryAttachTokenStaysLive(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)

	a, err := attachDirect(t, auth, signer, []byte("every-token-nonce-aaaaaaaaa"))
	require.NoError(t, err)
	b, err := attachDirect(t, auth, signer, []byte("every-token-nonce-bbbbbbbbb"))
	require.NoError(t, err)
	c, err := attachDirect(t, auth, signer, []byte("every-token-nonce-ccccccccc"))
	require.NoError(t, err)

	_, ok := auth.LookupToken(a.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(b.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(c.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_EarlierTokenAdmitsRPCs(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{SessionTTL: time.Minute, Now: clock.Now})
	mux := NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{sigs: map[uint32][]byte{0: {7}}}}))
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)
	signer := testutil.MustGenerateKey(t)
	authClient := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)

	first := attachAt(t, authClient, signer, []byte("early-rpc-nonce-aaaaaaaaaaa"), clock.Now().Unix())
	second := attachAt(t, authClient, signer, []byte("early-rpc-nonce-bbbbbbbbbbb"), clock.Now().Unix())
	client := rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL)

	resp, err := client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), first.SessionToken))
	require.NoError(t, err, "GetSignatures with the earlier header must succeed")
	require.Equal(t, []byte{7}, resp.Msg.Signatures[0])

	resp, err = client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), second.SessionToken))
	require.NoError(t, err)

	clock.Advance(10 * time.Second)
	_, err = client.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), first.SessionToken))
	require.NoError(t, err, "the earlier token stays valid after a later Attach")
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
	hostClient := rpcpbconnect.NewPeerAuthServiceClient(host.Client(), host.URL)
	renewNonce := []byte("live-renew-host-nonce-012345")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), renewNonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	renew := withSession(connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     renewNonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}), first.SessionToken)
	renewed, err := hostClient.Attach(context.Background(), renew)
	require.NoError(t, err)
	second := renewed.Msg
	require.Equal(t, int32(1), doorCalls.Load(), "live renewal must not re-run AllowsSender")
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "the renewed-from token stays valid")
	_, ok = auth.LookupToken(second.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_LiveReattachOnSettledEscrowRechecksDoor(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var doorCalls atomic.Int32
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(ctx context.Context, addr string) (bool, error) {
			doorCalls.Add(1)
			if EscrowIDFromContext(ctx) == "settled-escrow" {
				return false, bridge.ErrEscrowSettled
			}
			return addr == signer.Address(), nil
		},
	})
	mux := NewMux(auth, nil)
	open := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(open.Close)
	settled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), "settled-escrow")))
	}))
	t.Cleanup(settled.Close)

	first := attach(t, rpcpbconnect.NewPeerAuthServiceClient(open.Client(), open.URL), signer,
		[]byte("live-settled-door-nonce-01234"))
	require.Equal(t, int32(1), doorCalls.Load())

	ts := time.Now().Unix()
	nonce := []byte("live-settled-retry-nonce-0123")
	sig, err := transport.SignAttach(signer, testHostAddress, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = rpcpbconnect.NewPeerAuthServiceClient(settled.Client(), settled.URL).Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     testHostAddress,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "escrow settled")
	require.Equal(t, int32(2), doorCalls.Load(), "real escrow URL must re-run AllowsSender while the peer is live")
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok, "failed door Attach must not drop the live session")
}

func TestPeerAuth_LiveReattachOnOpenEscrowRechecksDoor(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var doorCalls atomic.Int32
	auth := newTestAuth(PeerAuthConfig{
		Allow: func(ctx context.Context, addr string) (bool, error) {
			doorCalls.Add(1)
			return addr == signer.Address(), nil
		},
	})
	mux := NewMux(auth, nil)
	a := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(a.Close)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(WithEscrowID(r.Context(), "other-open")))
	}))
	t.Cleanup(b.Close)

	first := attach(t, rpcpbconnect.NewPeerAuthServiceClient(a.Client(), a.URL), signer,
		[]byte("live-open-door-a-nonce-012345"))
	require.Equal(t, int32(1), doorCalls.Load())
	second := attach(t, rpcpbconnect.NewPeerAuthServiceClient(b.Client(), b.URL), signer,
		[]byte("live-open-door-b-nonce-012345"))
	require.Equal(t, int32(2), doorCalls.Load(), "open real escrow must still run AllowsSender")
	_, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok)
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
	require.Truef(t, stream.Receive(), "Watch on /sessions/_/rpc must admit a live token: %v", stream.Err())
}

func TestPeerAuth_SecondAttachOnAnyEscrowPathAddsToken(t *testing.T) {
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
	require.True(t, ok, "the first token stays valid")
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

func TestPeerAuth_SecondWatchOnSameTokenStaysOpen(t *testing.T) {
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

	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	second, err := client.Watch(secondCtx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.True(t, second.Receive(), "a second Watch on the same token stays open: %v", second.Err())
	secondCancel()
	_ = second.Close()

	_, ok := auth.LookupToken(token)
	require.True(t, ok, "closing the second Watch leaves the token live")

	cancel()
	_ = first.Close()
	for first.Receive() {
	}
	_, ok = auth.LookupToken(token)
	require.True(t, ok, "Watch termination must not drop the host session")

	watchAfterRelease(t, client, token)
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
	require.True(t, ok, "the earlier token stays valid")
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

func TestPeerAuth_WatchStaysOpenAcrossReattach(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
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
	_, ok := auth.LookupToken(second.SessionToken)
	require.True(t, ok)
	_, ok = auth.LookupToken(first.SessionToken)
	require.True(t, ok)
	require.True(t, stream.Receive(), "Watch on the earlier token keeps beating after a renewal: %v", stream.Err())
	cancel()
	_ = stream.Close()
}

func TestPeerAuth_WatchEndsAtTokenExpiry(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{Heartbeat: 20 * time.Millisecond, SessionTTL: 30 * time.Second, Now: clock.Now})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	signer := testutil.MustGenerateKey(t)
	token := attachAt(t, client, signer, []byte("watch-expiry-nonce-aaaaaaa"), clock.Now().Unix()).SessionToken

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	stream, err := client.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), token))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	clock.Advance(30*time.Second + sessionTokenSkew + time.Second)
	require.Eventually(t, func() bool {
		if stream.Receive() {
			return false
		}
		return strings.Contains(stream.Err().Error(), "session expired")
	}, time.Second, 10*time.Millisecond)
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
	require.Eventually(t, func() bool {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		if len(spy.setAt) < 2 {
			return false
		}
		armed := false
		for _, at := range spy.setAt {
			if !at.IsZero() {
				armed = true
			}
		}
		return armed && spy.setAt[len(spy.setAt)-1].IsZero()
	}, time.Second, 5*time.Millisecond, "Watch must arm a write deadline for Send and clear it afterward")
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

func TestPeerAuth_MaxSessionsDoesNotCapAttach(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{MaxSessions: 1})
	first := testutil.MustGenerateKey(t)
	second := testutil.MustGenerateKey(t)

	held, err := attachDirect(t, auth, first, []byte("max-sessions-nonce-aaaaaaa"))
	require.NoError(t, err)
	incoming, err := attachDirect(t, auth, second, []byte("max-sessions-nonce-bbbbbbb"))
	require.NoError(t, err, "MaxSessions caps concurrent Watches; Attach stores nothing")
	again, err := attachDirect(t, auth, first, []byte("max-sessions-nonce-ccccccc"))
	require.NoError(t, err)
	for _, tok := range [][]byte{held.SessionToken, incoming.SessionToken, again.SessionToken} {
		_, ok := auth.LookupToken(tok)
		require.True(t, ok)
	}
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

	clock.Advance(time.Second)
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-attach-nonce-dddddddddd"))
	require.NoError(t, err, "the bucket refills within a second")
}

func TestPeerAuth_ReplayWithoutTokenSpendsFloor(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	nonce := []byte("failed-known-attach-nonce-aa")
	first, err := attachDirect(t, auth, signer, nonce)
	require.NoError(t, err)

	req, err := signedAttach(signer, nonce, clock.Now().Unix())
	require.NoError(t, err)
	replayed, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), connect.NewRequest(req))
	require.NoError(t, err)
	require.Equal(t, first.SessionToken, replayed.Msg.SessionToken)

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("failed-known-attach-nonce-bb"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err),
		"a replay without a live token spends the anonymous floor")
	requireRetryAfter(t, err)
	require.Greater(t, spy.n.Load(), int32(0))
}

func TestPeerAuth_KnownPeerReattachDoesNotConsumeFloor(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})
	a := testutil.MustGenerateKey(t)
	first, err := attachDirectAt(t, auth, a, []byte("known-reattach-00000000xxxx"), clock.Now().Unix())
	require.NoError(t, err)
	token := first.SessionToken
	for i := 1; i < 5; i++ {
		req, err := signedAttach(a, []byte(fmt.Sprintf("known-reattach-%08dxxxx", i)), clock.Now().Unix()+int64(i))
		require.NoError(t, err)
		creq := connect.NewRequest(req)
		SetSessionHeader(creq.Header(), token)
		resp, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
		require.NoError(t, err)
		token = resp.Msg.SessionToken
	}
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-new-peer-nonce-bbbbbb"))
	require.NoError(t, err, "known-peer renewals must not occupy extra floor slots")

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("rate-new-peer-nonce-cccccc"))
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	requireRetryAfter(t, err)
}

func renewWithToken(auth *PeerAuthHandler, signer *signing.Secp256k1Signer, token, nonce []byte, ts int64) (*rpcpb.AttachResponse, error) {
	req, err := signedAttach(signer, nonce, ts)
	if err != nil {
		return nil, err
	}
	creq := connect.NewRequest(req)
	SetSessionHeader(creq.Header(), token)
	resp, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func TestPeerAuth_RenewalBucketRefusesBeforeVerify(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, signer, []byte("renew-bucket-nonce-0000xxxx"))
	require.NoError(t, err)
	token := first.SessionToken

	for i := 1; i <= renewalBurst; i++ {
		resp, err := renewWithToken(auth, signer, token, []byte(fmt.Sprintf("renew-bucket-nonce-%04dxxxx", i)), clock.Now().Unix())
		require.NoError(t, err, "renewal %d is inside the burst", i)
		token = resp.SessionToken
	}
	calls := spy.n.Load()
	_, err = renewWithToken(auth, signer, token, []byte("renew-bucket-nonce-overxxxx"), clock.Now().Unix())
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "too many attach renewals")
	requireRetryAfter(t, err)
	require.Equal(t, calls, spy.n.Load(), "the renewal bucket must refuse before ECDSA")

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("renew-bucket-anon-nonce-xxx"))
	require.NoError(t, err, "an empty renewal bucket leaves the anonymous floor alone")

	clock.Advance(time.Minute / renewalPerMin)
	_, err = renewWithToken(auth, signer, token, []byte("renew-bucket-nonce-refillxx"), clock.Now().Unix())
	require.NoError(t, err, "the renewal bucket refills")
}

func TestPeerAuth_RenewalBucketIsPerPeer(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	a := testutil.MustGenerateKey(t)
	b := testutil.MustGenerateKey(t)
	tokA, err := attachDirect(t, auth, a, []byte("renew-peer-a-nonce-0000xxx"))
	require.NoError(t, err)
	tokB, err := attachDirect(t, auth, b, []byte("renew-peer-b-nonce-0000xxx"))
	require.NoError(t, err)

	for i := 1; i <= renewalBurst; i++ {
		_, err := renewWithToken(auth, a, tokA.SessionToken, []byte(fmt.Sprintf("renew-peer-a-nonce-%04dxxx", i)), clock.Now().Unix())
		require.NoError(t, err)
	}
	_, err = renewWithToken(auth, a, tokA.SessionToken, []byte("renew-peer-a-nonce-overxxx"), clock.Now().Unix())
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))

	_, err = renewWithToken(auth, b, tokB.SessionToken, []byte("renew-peer-b-nonce-0001xxx"), clock.Now().Unix())
	require.NoError(t, err, "another peer's renewal bucket is untouched")
}

func TestPeerAuth_RenewalBucketOffWhenLimitsDisabled(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{Disabled: true},
		Now:    clock.Now,
	})
	signer := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, signer, []byte("renew-off-nonce-0000xxxxxx"))
	require.NoError(t, err)
	for i := 1; i <= 3*renewalBurst; i++ {
		_, err := renewWithToken(auth, signer, first.SessionToken, []byte(fmt.Sprintf("renew-off-nonce-%04dxxxxxx", i)), clock.Now().Unix())
		require.NoError(t, err)
	}
}

func TestPeerAuth_LiveTokenSkipsEmptyBucket(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 1, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, signer, []byte("live-token-skip-nonce-aaaa"))
	require.NoError(t, err)
	calls := spy.n.Load()

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("live-token-skip-nonce-bbbb"))
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, calls, spy.n.Load())

	req, err := signedAttach(signer, []byte("live-token-skip-nonce-cccc"), clock.Now().Unix()+1)
	require.NoError(t, err)
	creq := connect.NewRequest(req)
	SetSessionHeader(creq.Header(), first.SessionToken)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	require.NoError(t, err)
	require.Equal(t, calls+1, spy.n.Load(), "a live token still verifies, and does not spend the empty bucket")
}

func TestPeerAuth_LiveTokenWrongPeerChargesBucket(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})
	owner := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, owner, []byte("live-token-wrong-nonce-aa"))
	require.NoError(t, err)

	other := testutil.MustGenerateKey(t)
	req, err := signedAttach(other, []byte("live-token-wrong-nonce-bb"), clock.Now().Unix()+1)
	require.NoError(t, err)
	creq := connect.NewRequest(req)
	SetSessionHeader(creq.Header(), first.SessionToken)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	require.NoError(t, err, "a valid signature for a different peer is admitted once it pays the bucket")
	calls := spy.n.Load()
	require.Greater(t, calls, int32(1))

	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("live-token-wrong-nonce-cc"))
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, calls, spy.n.Load(), "the charged mismatch must leave the bucket empty before ECDSA")
}

func TestPeerAuth_LiveTokenWrongPeerRejectedWhenBucketEmpty(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 1, Now: clock.Now})
	owner := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, owner, []byte("live-token-empty-nonce-aa"))
	require.NoError(t, err)
	calls := spy.n.Load()

	other := testutil.MustGenerateKey(t)
	req, err := signedAttach(other, []byte("live-token-empty-nonce-bb"), clock.Now().Unix()+1)
	require.NoError(t, err)
	creq := connect.NewRequest(req)
	SetSessionHeader(creq.Header(), first.SessionToken)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, calls+1, spy.n.Load(), "the signature is checked, then the empty bucket refuses the other peer")
}

func TestPeerAuth_LiveTokenBadSignatureChargesBucket(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 2, Now: clock.Now})
	owner := testutil.MustGenerateKey(t)
	first, err := attachDirect(t, auth, owner, []byte("live-token-badsig-nonce-aa"))
	require.NoError(t, err)

	req, err := signedAttach(owner, []byte("live-token-badsig-nonce-bb"), clock.Now().Unix()+1)
	require.NoError(t, err)
	req.Signature[0] ^= 0xff
	creq := connect.NewRequest(req)
	SetSessionHeader(creq.Header(), first.SessionToken)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	calls := spy.n.Load()
	_, err = attachDirect(t, auth, testutil.MustGenerateKey(t), []byte("live-token-badsig-nonce-cc"))
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Equal(t, calls, spy.n.Load(), "a failed signature on a live token keeps the charge")
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

func TestPeerAuth_ReplayReturnsTheSameToken(t *testing.T) {
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	auth := newTestAuth(PeerAuthConfig{})
	signer := testutil.MustGenerateKey(t)
	ts := auth.now().Unix()
	first, err := attachDirectAt(t, auth, signer, nonce, ts)
	require.NoError(t, err)
	second, err := attachDirectAt(t, auth, signer, nonce, ts)
	require.NoError(t, err)
	require.Equal(t, first.SessionToken, second.SessionToken)
	require.NotEqual(t, nonce, first.SessionToken)
	peer, ok := auth.LookupToken(first.SessionToken)
	require.True(t, ok)
	require.Equal(t, signer.Address(), peer)
}

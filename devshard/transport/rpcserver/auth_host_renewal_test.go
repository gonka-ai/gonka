package rpcserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
)

// hostRenewalAuth mirrors production: the door escrow admits the signer and
// `_` is not an escrow, so the roster check on it fails as "not open".
func hostRenewalAuth(clock *testClock, ttl time.Duration, floor int, doorCalls *atomic.Int32) *PeerAuthHandler {
	return newTestAuth(PeerAuthConfig{
		SessionTTL:        ttl,
		Now:               clock.Now,
		AttachFloorPerMin: floor,
		Allow: func(ctx context.Context, addr string) (bool, error) {
			doorCalls.Add(1)
			if EscrowIDFromContext(ctx) == transport.HostRPCEscrowID {
				return false, bridge.ErrEscrowNotFound
			}
			return true, nil
		},
	})
}

func attachOnHostURL(t *testing.T, auth *PeerAuthHandler, signer *signing.Secp256k1Signer, nonce []byte, ts int64, token []byte) (*connect.Response[rpcpb.AttachResponse], error) {
	t.Helper()
	msg, err := signedAttach(signer, nonce, ts)
	require.NoError(t, err)
	req := connect.NewRequest(msg)
	if len(token) > 0 {
		SetSessionHeader(req.Header(), token)
	}
	return auth.Attach(WithEscrowID(context.Background(), transport.HostRPCEscrowID), req)
}

func requireHostRenewalRefused(t *testing.T, resp *connect.Response[rpcpb.AttachResponse], err error, msg string) {
	t.Helper()
	require.Error(t, err)
	require.Nil(t, resp, "a refused renewal must not issue a token")
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "%v", err)
	require.Contains(t, err.Error(), msg)
	require.NotContains(t, err.Error(), "escrow is not open")
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	require.Empty(t, ce.Meta().Get(transport.HeaderDevshardError),
		"versiond must not charge the origin for a token that ran out")
}

func TestPeerAuth_ExpiredRenewalOnHostURLIsUnauthenticated(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	const ttl = 30 * time.Second
	var doorCalls atomic.Int32
	auth := hostRenewalAuth(clock, ttl, 0, &doorCalls)
	signer := testutil.MustGenerateKey(t)

	first, err := attachDirect(t, auth, signer, []byte("host-renew-door-nonce-000001"))
	require.NoError(t, err)
	require.Equal(t, int32(1), doorCalls.Load())

	clock.Advance(ttl + sessionTokenSkew + time.Second)
	_, ok := auth.LookupToken(first.SessionToken)
	require.False(t, ok, "the token must be past its TTL for this test")

	resp, err := attachOnHostURL(t, auth, signer, []byte("host-renew-expired-nonce-001"), clock.Now().Unix(), first.SessionToken)
	requireHostRenewalRefused(t, resp, err, "session expired")
	require.Equal(t, int32(1), doorCalls.Load(), "`_` must not run the roster check")

	again, err := attachDirect(t, auth, signer, []byte("host-renew-door-nonce-000002"))
	require.NoError(t, err, "the client recovers by Attaching on a real escrow")
	_, ok = auth.LookupToken(again.SessionToken)
	require.True(t, ok)
}

func TestPeerAuth_LiveRenewalOnHostURLStillRenews(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	const ttl = 30 * time.Second
	var doorCalls atomic.Int32
	auth := hostRenewalAuth(clock, ttl, 0, &doorCalls)
	signer := testutil.MustGenerateKey(t)

	first, err := attachDirect(t, auth, signer, []byte("host-renew-live-door-nonce01"))
	require.NoError(t, err)
	clock.Advance(ttl * 3 / 4)

	resp, err := attachOnHostURL(t, auth, signer, []byte("host-renew-live-nonce-000001"), clock.Now().Unix(), first.SessionToken)
	require.NoError(t, err)
	_, ok := auth.LookupToken(resp.Msg.SessionToken)
	require.True(t, ok)
	require.Equal(t, int32(1), doorCalls.Load(), "a live renewal on `_` skips the door")
}

func TestPeerAuth_AnonymousAttachOnHostURLSpendsNothing(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	var doorCalls atomic.Int32
	auth := hostRenewalAuth(clock, time.Minute, 1, &doorCalls)
	signer := testutil.MustGenerateKey(t)

	for i := 0; i < 3; i++ {
		nonce := []byte("host-anon-attach-nonce-00000" + string(rune('a'+i)))
		resp, err := attachOnHostURL(t, auth, signer, nonce, clock.Now().Unix(), nil)
		requireHostRenewalRefused(t, resp, err, "handshake required")
	}
	require.Zero(t, doorCalls.Load(), "`_` must not run the roster check")

	_, err := attachDirect(t, auth, signer, []byte("host-anon-door-nonce-0000001"))
	require.NoError(t, err, "a refused `_` Attach must not spend the anonymous bucket (burst 1)")
}

func TestPeerAuth_ForeignTokenRenewalOnHostURLIsRefused(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	var doorCalls atomic.Int32
	auth := hostRenewalAuth(clock, time.Minute, 0, &doorCalls)
	owner := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)

	owned, err := attachDirect(t, auth, owner, []byte("host-foreign-owner-nonce-001"))
	require.NoError(t, err)
	require.Equal(t, int32(1), doorCalls.Load())

	resp, err := attachOnHostURL(t, auth, other, []byte("host-foreign-other-nonce-001"), clock.Now().Unix(), owned.SessionToken)
	requireHostRenewalRefused(t, resp, err, "handshake required")
	require.Equal(t, int32(1), doorCalls.Load(), "another signer's token must not reach the roster check on `_`")
}

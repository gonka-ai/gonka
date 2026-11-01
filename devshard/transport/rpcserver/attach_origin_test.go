package rpcserver

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
)

func TestAttachOriginKey(t *testing.T) {
	hdr := func(v string) http.Header {
		h := make(http.Header)
		if v != "" {
			h.Set(originIPHeader, v)
		}
		return h
	}
	require.Equal(t, "", attachOriginKey(nil))
	require.Equal(t, "", attachOriginKey(hdr("")))
	require.Equal(t, "", attachOriginKey(hdr("not-an-ip")))
	require.Equal(t, "", attachOriginKey(hdr("192.0.2.1, 198.51.100.1")))
	require.Equal(t, "192.0.2.1", attachOriginKey(hdr("192.0.2.1")))
	require.Equal(t, "192.0.2.1", attachOriginKey(hdr("  192.0.2.1  ")))
	require.Equal(t, "192.0.2.1", attachOriginKey(hdr("192.0.2.1:443")))
	require.Equal(t, "192.0.2.1", attachOriginKey(hdr("::ffff:192.0.2.1")))
	require.Equal(t, "2001:db8:1:2::/64", attachOriginKey(hdr("2001:db8:1:2::1")))
	require.Equal(t, "2001:db8:1:2::/64", attachOriginKey(hdr("2001:db8:1:2:ffff::9")))
	require.Equal(t, "2001:db8:1:3::/64", attachOriginKey(hdr("2001:db8:1:3::1")))
	require.Equal(t, "2001:db8:1:2::/64", attachOriginKey(hdr("[2001:db8:1:2::1]:443")))
}

func TestOriginShareStaysUnderProcessCap(t *testing.T) {
	require.Equal(t, 10.0, originShare(10, 50))
	require.Equal(t, 4.0, originShare(4, 50))
	require.Equal(t, 1.0, originShare(10, 8))
	require.Equal(t, 1.0, originShare(10, 2))
	require.Equal(t, 1.0, originShare(10, 1))
}

func postAttachIP(auth *PeerAuthHandler, signer *signing.Secp256k1Signer, nonce []byte, ip string) (*rpcpb.AttachResponse, error) {
	req, err := signedAttach(signer, nonce, auth.now().Unix())
	if err != nil {
		return nil, err
	}
	creq := connect.NewRequest(req)
	if ip != "" {
		creq.Header().Set(originIPHeader, ip)
	}
	resp, err := auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func attachFromIP(t *testing.T, auth *PeerAuthHandler, signer *signing.Secp256k1Signer, nonce []byte, ip string) (*rpcpb.AttachResponse, error) {
	t.Helper()
	return postAttachIP(auth, signer, nonce, ip)
}

func burnAttachFromIP(t *testing.T, auth *PeerAuthHandler, ip string, n int) error {
	t.Helper()
	signer := testutil.MustGenerateKey(t)
	nonce := []byte(fmt.Sprintf("origin-burn-%04d-xxxxx", n))
	req, err := signedAttach(signer, nonce, auth.now().Unix())
	require.NoError(t, err)
	req.Signature[0] ^= 0xff
	creq := connect.NewRequest(req)
	if ip != "" {
		creq.Header().Set(originIPHeader, ip)
	}
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	return err
}

func processTokens(auth *PeerAuthHandler) float64 {
	auth.attachMu.Lock()
	defer auth.attachMu.Unlock()
	return auth.attachBucket.tokens
}

func TestPeerAuth_OriginAttachIsolatesAddresses(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})

	for i := 0; i < defaultOriginAttachBurst; i++ {
		err := burnAttachFromIP(t, auth, "203.0.113.10", i)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	}
	calls := spy.n.Load()
	require.Equal(t, int32(defaultOriginAttachBurst), calls)
	before := processTokens(auth)
	require.Equal(t, float64(defaultAttachBurst-defaultOriginAttachBurst), before)

	err := burnAttachFromIP(t, auth, "203.0.113.10", defaultOriginAttachBurst)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), attachOriginLimitMessage)
	requireRetryAfter(t, err)
	require.Equal(t, calls, spy.n.Load(), "an empty origin bucket refuses before ECDSA")
	require.Equal(t, before, processTokens(auth), "the origin refusal must not spend the process floor")

	_, err = attachFromIP(t, auth, testutil.MustGenerateKey(t), []byte("origin-other-addr-nonce-a"), "203.0.113.11")
	require.NoError(t, err, "another address still attaches")
	require.Equal(t, before-1, processTokens(auth))
}

func TestPeerAuth_OriginAttachRefills(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	for i := 0; i < defaultOriginAttachBurst; i++ {
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, "203.0.113.20", i)))
	}
	calls := spy.n.Load()
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(burnAttachFromIP(t, auth, "203.0.113.20", 99)))
	require.Equal(t, calls, spy.n.Load())

	clock.Advance(time.Second)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, "203.0.113.20", 100)))
	require.Equal(t, calls+1, spy.n.Load(), "one second refills the origin bucket")
}

func TestPeerAuth_OriginAttachSharesIPv6Prefix(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	for i := 0; i < defaultOriginAttachBurst; i++ {
		ip := "2001:db8:1:2::1"
		if i%2 == 1 {
			ip = "2001:db8:1:2:ffff::9"
		}
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, ip, i)))
	}
	calls := spy.n.Load()
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(burnAttachFromIP(t, auth, "2001:db8:1:2::2", 50)))
	require.Equal(t, calls, spy.n.Load(), "the same /64 shares the origin bucket")

	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, "2001:db8:1:3::1", 51)))
	require.Equal(t, calls+1, spy.n.Load(), "a different /64 has its own bucket")
}

func TestPeerAuth_OriginAttachMappedIPv4SharesBucket(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	for i := 0; i < defaultOriginAttachBurst; i++ {
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, "192.0.2.10", i)))
	}
	calls := spy.n.Load()
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(burnAttachFromIP(t, auth, "::ffff:192.0.2.10", 50)))
	require.Equal(t, calls, spy.n.Load())
}

func TestPeerAuth_MissingOriginUsesProcessFloorOnly(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	for i := 0; i < defaultOriginAttachBurst+1; i++ {
		err := burnAttachFromIP(t, auth, "", i)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "a missing header is not an origin key")
	}
	for i := 0; i < defaultOriginAttachBurst+1; i++ {
		err := burnAttachFromIP(t, auth, "not-an-ip", 100+i)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "an unparseable header is not an origin key")
	}
	require.Equal(t, int32(2*(defaultOriginAttachBurst+1)), spy.n.Load())
	require.Equal(t, float64(defaultAttachBurst-2*(defaultOriginAttachBurst+1)), processTokens(auth))
}

func TestPeerAuth_LiveTokenSkipsEmptyOrigin(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	signer := testutil.MustGenerateKey(t)
	first, err := attachFromIP(t, auth, signer, []byte("origin-live-nonce-000xxxx"), "203.0.113.40")
	require.NoError(t, err)
	for i := 1; i < defaultOriginAttachBurst; i++ {
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, "203.0.113.40", i)))
	}
	calls := spy.n.Load()
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(burnAttachFromIP(t, auth, "203.0.113.40", 90)))
	require.Equal(t, calls, spy.n.Load())

	req, err := signedAttach(signer, []byte("origin-live-nonce-001xxxx"), clock.Now().Unix()+1)
	require.NoError(t, err)
	creq := connect.NewRequest(req)
	creq.Header().Set(originIPHeader, "203.0.113.40")
	SetSessionHeader(creq.Header(), first.SessionToken)
	_, err = auth.Attach(WithEscrowID(context.Background(), testEscrowID), creq)
	require.NoError(t, err, "a live token does not spend the origin bucket")
	require.Equal(t, calls+1, spy.n.Load())
}

type gateVerifier struct {
	inner signing.Verifier
	enter chan struct{}
	hold  <-chan struct{}
	n     atomic.Int32
}

func (v *gateVerifier) RecoverAddress(message, signature []byte) (string, error) {
	v.n.Add(1)
	if v.enter != nil {
		v.enter <- struct{}{}
	}
	if v.hold != nil {
		<-v.hold
	}
	return v.inner.RecoverAddress(message, signature)
}

func TestPeerAuth_OriginAttachInFlightIsPerAddress(t *testing.T) {
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)

	gate := &gateVerifier{
		inner: signing.NewSecp256k1Verifier(),
		enter: make(chan struct{}, defaultOriginAttachInFlight+2),
		hold:  hold,
	}
	auth := NewPeerAuthHandler(gate, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100})
	signers := make([]*signing.Secp256k1Signer, defaultOriginAttachInFlight)
	nonces := make([][]byte, defaultOriginAttachInFlight)
	for i := 0; i < defaultOriginAttachInFlight; i++ {
		signers[i] = testutil.MustGenerateKey(t)
		nonces[i] = []byte(fmt.Sprintf("origin-flight-%02d-xxxx", i))
	}
	extra := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	var wg sync.WaitGroup
	errs := make(chan error, defaultOriginAttachInFlight)
	for i := 0; i < defaultOriginAttachInFlight; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := postAttachIP(auth, signers[i], nonces[i], "203.0.113.50")
			errs <- err
		}(i)
	}
	for i := 0; i < defaultOriginAttachInFlight; i++ {
		select {
		case <-gate.enter:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for in-flight Attach")
		}
	}
	calls := gate.n.Load()

	done := make(chan error, 1)
	go func() {
		_, err := postAttachIP(auth, extra, []byte("origin-flight-over-xxxx"), "203.0.113.50")
		done <- err
	}()
	select {
	case err := <-done:
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
		require.Equal(t, calls, gate.n.Load(), "a full origin in-flight set refuses before ECDSA")
	case <-time.After(2 * time.Second):
		t.Fatal("the extra Attach blocked in VerifyAttach")
	}

	otherDone := make(chan error, 1)
	go func() {
		_, err := postAttachIP(auth, other, []byte("origin-flight-other-xxx"), "203.0.113.51")
		otherDone <- err
	}()
	select {
	case <-gate.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("another address did not reach VerifyAttach")
	}
	release()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, <-otherDone)
}

func TestOriginAttachTableEvictsIdle(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{Now: clock.Now})
	auth.attachMu.Lock()
	for i := 0; i < maxOriginAttachEntries; i++ {
		e := auth.originEntryLocked(fmt.Sprintf("k-%d", i))
		require.NotNil(t, e)
	}
	require.Equal(t, maxOriginAttachEntries, auth.origins.order.Len())
	fresh := auth.originEntryLocked("fresh")
	auth.attachMu.Unlock()
	require.NotNil(t, fresh)
	require.Equal(t, maxOriginAttachEntries-originAttachEvictBatch+1, auth.origins.order.Len())
	require.NotContains(t, auth.origins.byKey, "k-0")
	require.Contains(t, auth.origins.byKey, "fresh")
}

func TestOriginAttachTableKeepsInFlight(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := newTestAuth(PeerAuthConfig{Now: clock.Now})
	auth.attachMu.Lock()
	for i := 0; i < maxOriginAttachEntries; i++ {
		e := auth.originEntryLocked(fmt.Sprintf("k-%d", i))
		require.NotNil(t, e)
		if i < originAttachEvictBatch {
			e.bucket.inFlight = 1
		}
	}
	fresh := auth.originEntryLocked("fresh")
	auth.attachMu.Unlock()
	require.Nil(t, fresh, "a batch of in-flight entries is not evicted, and the new key is not inserted")
	require.Contains(t, auth.origins.byKey, "k-0")
	require.Equal(t, maxOriginAttachEntries, auth.origins.order.Len())

	_, err := attachFromIP(t, auth, testutil.MustGenerateKey(t), []byte("origin-table-full-nonce"), "198.51.100.8")
	require.NoError(t, err, "a full table leaves the request on the process floor")
	require.NotContains(t, auth.origins.byKey, attachOriginKey(httpHeader("198.51.100.8")))
}

func httpHeader(ip string) http.Header {
	h := make(http.Header)
	h.Set(originIPHeader, ip)
	return h
}

func TestPeerAuth_AttachRefusalStatsStayApart(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{AttachFloorPerMin: 100, Now: clock.Now})
	for i := 0; i < defaultOriginAttachBurst; i++ {
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(burnAttachFromIP(t, auth, "203.0.113.80", i)))
	}
	err := burnAttachFromIP(t, auth, "203.0.113.80", defaultOriginAttachBurst)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	clock.Advance(time.Minute)
	snap := auth.Traffic().Snapshot(clock.Now())
	require.Equal(t, uint64(1), snap.Host.Attach.Banned)
	require.Equal(t, uint64(1), snap.Host.Attach.BannedOrigin)
	require.Equal(t, uint64(0), snap.Host.Attach.BannedFloor)
	require.Equal(t, uint64(1), zoneBanned(snap, transport.RPCZoneAttachOrigin))
	require.Equal(t, uint64(0), zoneBanned(snap, transport.RPCZoneAttachFloor))
}

func TestPeerAuth_ProcessFloorStatIsNotOrigin(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	auth := NewPeerAuthHandler(signing.NewSecp256k1Verifier(), testHostAddress, PeerAuthConfig{AttachFloorPerMin: 1, Now: clock.Now})
	_, err := attachFromIP(t, auth, testutil.MustGenerateKey(t), []byte("floor-stat-nonce-aaaaaaa"), "")
	require.NoError(t, err)
	err = burnAttachFromIP(t, auth, "", 1)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.NotContains(t, err.Error(), "for this address")
	clock.Advance(time.Minute)
	snap := auth.Traffic().Snapshot(clock.Now())
	require.Equal(t, uint64(1), snap.Host.Attach.BannedFloor)
	require.Equal(t, uint64(0), snap.Host.Attach.BannedOrigin)
}

func zoneBanned(snap transport.RPCStatsSnapshot, zone string) uint64 {
	for _, z := range snap.Host.Zones {
		if z.Zone == zone {
			return z.Banned
		}
	}
	return 0
}

func TestPeerAuth_DisabledLimitsSkipOrigin(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{Disabled: true},
	})
	for i := 0; i < defaultOriginAttachBurst+2; i++ {
		_, err := attachFromIP(t, auth, testutil.MustGenerateKey(t), []byte(fmt.Sprintf("origin-off-%04d-xxxxxx", i)), "203.0.113.70")
		require.NoError(t, err)
	}
}

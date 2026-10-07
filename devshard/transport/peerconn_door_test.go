package transport

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	devtest "devshard/internal/testutil"
	"devshard/transport/rpcpb"
)

func TestIsDeadDoorError(t *testing.T) {
	t.Parallel()
	require.False(t, isDeadDoorError(nil))
	require.False(t, isDeadDoorError(errors.New("escrow settled")))
	require.False(t, isDeadDoorError(connect.NewError(connect.CodeUnavailable, errors.New("escrow settled"))))
	require.False(t, isDeadDoorError(connect.NewError(connect.CodeFailedPrecondition, errors.New("host shutting down"))))
	require.False(t, isDeadDoorError(connect.NewError(connect.CodeFailedPrecondition, errors.New("session version conflict"))))
	require.False(t, isDeadDoorError(connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))))

	settled := connect.NewError(connect.CodeFailedPrecondition, errors.New("escrow settled"))
	require.True(t, isDeadDoorError(settled))
	notOpen := connect.NewError(connect.CodeFailedPrecondition, errors.New("escrow is not open on this host"))
	require.True(t, isDeadDoorError(notOpen))

	headerOnly := connect.NewError(connect.CodeFailedPrecondition, errors.New("gone"))
	headerOnly.Meta().Set(HeaderDevshardError, DevshardErrorEscrowNotFound)
	require.True(t, isDeadDoorError(headerOnly))
	settledHeader := connect.NewError(connect.CodeFailedPrecondition, errors.New("gone"))
	settledHeader.Meta().Set(HeaderDevshardError, DevshardErrorEscrowSettled)
	require.True(t, isDeadDoorError(settledHeader))
}

func TestValidAttachDoorID(t *testing.T) {
	t.Parallel()
	require.False(t, validAttachDoorID(""))
	require.False(t, validAttachDoorID(" "))
	require.False(t, validAttachDoorID(HostRPCEscrowID))
	require.True(t, validAttachDoorID("42"))
	require.True(t, validAttachDoorID("99"))
}

func TestPeerConn_PickDoorPrefersLiveRefs(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1doorpick",
		DoorEscrowID: "42",
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)

	require.Equal(t, "42", pc.pickDoor(), "config door is the fallback before any RPCClient")
	pc.addDoor(HostRPCEscrowID)
	require.Equal(t, "42", pc.pickDoor(), "HostRPCEscrowID must never be a door")

	pc.addDoor("42")
	pc.addDoor("99")
	require.Equal(t, "42", pc.pickDoor(), "prefer the original door while it is live")

	pc.killDoor("42")
	require.Equal(t, "99", pc.pickDoor())

	pc.killDoor("99")
	require.Empty(t, pc.pickDoor())
	require.False(t, pc.hasAttachDoor())
}

func TestPeerConn_DropDoorRemovesCandidate(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1doordrop",
		DoorEscrowID: "42",
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	pc.addDoor("42")
	pc.addDoor("99")
	pc.dropDoor("42")
	require.Equal(t, "99", pc.pickDoor())
	pc.dropDoor("99")
	require.Empty(t, pc.pickDoor(), "after the first addDoor, an empty door set is no door")
}

func TestPeerConn_KillConfigDoorDoesNotFallBack(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1doorkill",
		DoorEscrowID: "42",
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	pc.killDoor("42")
	require.Empty(t, pc.pickDoor(), "a settled config door must not be retried")
	pc.addDoor("99")
	require.Equal(t, "99", pc.pickDoor())
}

func TestPeerConn_PreferDoorAimsWaitReadyEscrow(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1prefer",
		DoorEscrowID: "42",
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	pc.addDoor("42")
	pc.addDoor("99")
	require.Equal(t, "42", pc.pickDoor())
	pc.preferDoor("99")
	require.Equal(t, "99", pc.pickDoor())
	pc.killDoor("99")
	pc.preferDoor("99")
	require.Equal(t, "42", pc.pickDoor(), "preferDoor must not resurrect a killed door")
}

func TestRPCClient_WaitReadyPrefersOwnEscrow(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1waitprefer",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	_ = NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointChat))
	rpc99 := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "99", signer), pc, ParseRPCEndpoints(EndpointChat))
	require.Equal(t, "42", pc.pickDoor())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_ = rpc99.WaitReady(ctx)
	require.Equal(t, "99", pc.pickDoor())
}

func TestAcquirePeerConn_StartsOnFirstRPCClient(t *testing.T) {
	var nSleep atomic.Int32
	peer := devtest.MustGenerateKey(t)
	cfg := PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1latestart",
		DoorEscrowID: "42",
		Signer:       peer,
		DirectMux:    true,
		BackoffMin:   time.Hour,
		BackoffMax:   time.Hour,
		Jitter:       func(d time.Duration) time.Duration { return d },
		Sleep: func(ctx context.Context, d time.Duration) error {
			nSleep.Add(1)
			if d > 0 {
				return ctx.Err()
			}
			return nil
		},
	}
	pc := acquirePeerConn(cfg)
	time.Sleep(30 * time.Millisecond)
	require.Zero(t, nSleep.Load(), "acquirePeerConn must not Start the attach loop")
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", peer), pc, ParseRPCEndpoints(EndpointSignatures))
	t.Cleanup(rpc.Close)
	require.Eventually(t, func() bool { return nSleep.Load() > 0 }, time.Second, 5*time.Millisecond)
}

func TestRPCClient_WaitReadyFailsWhenDoorsExhausted(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1nodoor",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointChat))
	pc.killDoor("42")

	start := time.Now()
	err := rpc.WaitReady(context.Background())
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.Contains(t, err.Error(), "no attach door")
	require.Less(t, time.Since(start), 300*time.Millisecond)

	rpc99 := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "99", signer), pc, ParseRPCEndpoints(EndpointChat))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start = time.Now()
	err = rpc99.WaitReady(ctx)
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.GreaterOrEqual(t, time.Since(start), 80*time.Millisecond, "a live door must wait on Attach, not fail immediately")
}

func TestRPCClient_WaitReadyWakesOnPublishToken(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1waitwake",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointChat))

	errCh := make(chan error, 1)
	go func() {
		errCh <- rpc.WaitReady(context.Background())
	}()
	select {
	case err := <-errCh:
		t.Fatalf("WaitReady returned before token: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	start := time.Now()
	pc.publishToken([]byte("tok"), time.Now().Add(time.Hour))
	pc.setState(stateReady)
	select {
	case err := <-errCh:
		require.NoError(t, err)
		require.Less(t, time.Since(start), 50*time.Millisecond, "must wake on publishToken, not a 20 ms poll")
	case <-time.After(time.Second):
		t.Fatal("WaitReady did not wake after publishToken")
	}
}

func TestRPCClient_WaitReadyWakesWhenLastDoorKilled(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1waitkill",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointChat))

	errCh := make(chan error, 1)
	go func() {
		errCh <- rpc.WaitReady(context.Background())
	}()
	select {
	case err := <-errCh:
		t.Fatalf("WaitReady returned before killDoor: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	start := time.Now()
	pc.killDoor("42")
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, ErrPeerNotReady)
		require.Contains(t, err.Error(), "no attach door")
		require.Less(t, time.Since(start), 50*time.Millisecond)
	case <-time.After(time.Second):
		t.Fatal("WaitReady did not wake after last door was killed")
	}
}

func TestRPCClient_WaitReadyFailsWhenDoorIsHostEscrow(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1hostdoor",
		DoorEscrowID: HostRPCEscrowID,
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", HostRPCEscrowID, signer), pc, ParseRPCEndpoints(EndpointChat))
	start := time.Now()
	err := rpc.WaitReady(context.Background())
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.Contains(t, err.Error(), "no attach door")
	require.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestRPCClient_CloseDropsAttachDoor(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1closedoor",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc42 := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointSignatures))
	_ = NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "99", signer), pc, ParseRPCEndpoints(EndpointSignatures))
	require.Equal(t, "42", pc.pickDoor())
	pc.refs.Store(2)
	rpc42.Close()
	require.Equal(t, "99", pc.pickDoor())
}

func TestTokenRequestFailsWithoutReadySession(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1tokendoor",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointSignatures))
	pc.killDoor("42")
	_, err := tokenRequest(rpc, &rpcpb.GetSignaturesRequest{Nonce: 1})
	require.ErrorIs(t, err, ErrPeerNotReady)
}

func TestPeerConn_NoDoorWaitsForAddDoor(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	const backoffMin = 30 * time.Millisecond
	var attempts atomic.Int32
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1nodoorspin",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
		BackoffMin:   backoffMin,
		BackoffMax:   time.Second,
		Jitter:       func(d time.Duration) time.Duration { return d },
		Sleep: func(ctx context.Context, d time.Duration) error {
			attempts.Add(1)
			return sleepContext(ctx, d)
		},
	})
	t.Cleanup(pc.Close)
	pc.killDoor("42")
	pc.Start()

	time.Sleep(8 * backoffMin)
	require.Zero(t, attempts.Load(), "no door must not retry at BackoffMin")
	require.Equal(t, int32(stateUnauthenticated), pc.state.Load())

	start := time.Now()
	pc.addDoor("99")
	require.Eventually(t, func() bool { return attempts.Load() > 0 }, backoffMin, 5*time.Millisecond)
	require.Less(t, time.Since(start), backoffMin, "addDoor must wake the attach loop before BackoffMax")
}

func TestPeerConn_DoorAuthClientCachedPerEscrow(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1doorcache",
		DoorEscrowID: "42",
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	before := pc.doorClientsBuilt
	_ = pc.doorAuthClient("99")
	_ = pc.doorAuthClient("99")
	require.Equal(t, before+1, pc.doorClientsBuilt, "a non-creator door is built once")
	pc.killDoor("99")
	_ = pc.doorAuthClient("99")
	require.Equal(t, before+2, pc.doorClientsBuilt, "killing the door drops its client")
}

func TestRPCClient_CloneKeepsDoorAfterOriginalClose(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:      "http://127.0.0.1:1",
		HostAddress:  "gonka1clonedoor",
		DoorEscrowID: "42",
		Signer:       signer,
		DirectMux:    true,
	})
	t.Cleanup(pc.Close)
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "42", signer), pc, ParseRPCEndpoints(EndpointChat))
	clone := rpc.cloneWithSigner(devtest.MustGenerateKey(t), time.Second)
	other, ok := rpc.WithoutAdmission().(*RPCClient)
	require.True(t, ok)
	pc.refs.Store(2)

	rpc.Close()
	require.Equal(t, "42", pc.pickDoor(), "clones still hold the door")
	pc.publishToken([]byte("tok"), time.Now().Add(time.Hour))
	pc.setState(stateReady)
	require.NoError(t, clone.WaitReady(context.Background()))

	clone.Close()
	require.Equal(t, "42", pc.pickDoor(), "one clone remains")
	other.Close()
	require.Empty(t, pc.pickDoor(), "the last clone releases the door")
	require.False(t, pc.hasAttachDoor())
}

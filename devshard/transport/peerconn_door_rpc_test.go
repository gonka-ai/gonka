package transport_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/host"
	devtest "devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
	"devshard/types"
)

const doorChatSSE = "data: {\"devshard_receipt\":{\"state_sig\":\"c2ln\",\"state_hash\":\"aGFzaA==\",\"nonce\":1,\"receipt\":\"cmVjZWlwdA==\",\"confirmed_at\":1000}}\n\ndata: [DONE]\n\n"

type routedPeerRPC struct {
	srv     *httptest.Server
	auth    *rpcserver.PeerAuthHandler
	mu      sync.Mutex
	settled map[string]error
	allows  []string
}

func startRoutedPeerRPC(t *testing.T, hostAddr string, lookup rpcserver.SessionLookup) *routedPeerRPC {
	t.Helper()
	h := &routedPeerRPC{settled: map[string]error{}}
	h.auth = rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat: 50 * time.Millisecond,
		Allow: func(ctx context.Context, addr string) (bool, error) {
			_ = addr
			id := rpcserver.EscrowIDFromContext(ctx)
			h.mu.Lock()
			h.allows = append(h.allows, id)
			err := h.settled[id]
			h.mu.Unlock()
			if err != nil {
				return false, err
			}
			if id == transport.HostRPCEscrowID {
				return false, bridge.ErrEscrowNotFound
			}
			return true, nil
		},
	})
	mux := rpcserver.NewMux(h.auth, rpcserver.NewSessionHandler(lookup))
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escrow, procedure, ok := cutSessionRPC(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		u := *r.URL
		u.Path = procedure
		r2 := r.WithContext(rpcserver.WithEscrowID(r.Context(), escrow))
		r2.URL = &u
		mux.ServeHTTP(w, r2)
	}))
	t.Cleanup(h.srv.Close)
	t.Cleanup(h.auth.Close)
	return h
}

func (h *routedPeerRPC) settle(id string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.settled[id] = err
}

func (h *routedPeerRPC) attachDoors() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string(nil), h.allows...)
	return out
}

type doorChatCore struct{}

func (doorChatCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{1: []byte("sig-99")}, nil
}

func (doorChatCore) AllowsSender(string) bool { return true }

func (doorChatCore) IsOwner(string) bool { return true }

func (doorChatCore) ServeInference(_ context.Context, call transport.InferenceCall) error {
	if call.Sink == nil {
		return nil
	}
	if _, err := call.Sink.Write([]byte(doorChatSSE)); err != nil {
		return err
	}
	call.Sink.Flush()
	return nil
}

type doorLookup struct {
	live string
	core rpcserver.SessionCore
}

func (l doorLookup) SessionServerExisting(id string) (rpcserver.SessionCore, error) {
	if id == l.live {
		return l.core, nil
	}
	return nil, bridge.ErrEscrowSettled
}

func (l doorLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}

func (l doorLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}

func (l doorLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
	return l.SessionForParticipant(id, addr)
}

func newSharedDoorClients(t *testing.T, srv *httptest.Server, hostAddr string, peer signing.Signer, door, live string) (*transport.PeerConn, *transport.RPCClient, *transport.RPCClient) {
	t.Helper()
	cfg := transport.PeerConnConfig{
		BaseURL:      srv.URL,
		RoutePrefix:  transport.DefaultRoutePrefix(),
		DoorEscrowID: door,
		HostAddress:  hostAddr,
		Signer:       peer,
		WatchStale:   time.Minute,
		BackoffMin:   20 * time.Millisecond,
		BackoffMax:   200 * time.Millisecond,
	}
	pc := transport.NewPeerConn(cfg)
	t.Cleanup(pc.Close)
	set := transport.ParseRPCEndpoints(transport.EndpointChat + "," + transport.EndpointSignatures)
	rpcDoor := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, door, peer), pc, set)
	rpcLive := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, live, peer), pc, set)
	return pc, rpcDoor, rpcLive
}

func TestPeerConn_AttachRetriesLiveEscrowAfterSettledDoor(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	lookup := doorLookup{live: "99", core: doorChatCore{}}
	h := startRoutedPeerRPC(t, hostAddr, lookup)
	h.settle("42", bridge.ErrEscrowSettled)

	pc, _, rpc99 := newSharedDoorClients(t, h.srv, hostAddr, peer, "42", "99")
	pc.Start()
	waitPeerReady(t, pc)

	require.NotContains(t, h.attachDoors(), transport.HostRPCEscrowID, "first Attach must not use /sessions/_/rpc")
	require.Contains(t, h.attachDoors(), "42")
	require.Contains(t, h.attachDoors(), "99")

	got, err := rpc99.GetSignatures(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, map[uint32][]byte{1: []byte("sig-99")}, got)

	var stream strings.Builder
	resp, err := rpc99.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, &stream, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(1), resp.Nonce)
}

func TestPeerConn_ReattachAfterWatchKillUsesLiveEscrow(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	lookup := doorLookup{live: "99", core: doorChatCore{}}
	h := startRoutedPeerRPC(t, hostAddr, lookup)

	pc, _, rpc99 := newSharedDoorClients(t, h.srv, hostAddr, peer, "42", "99")
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	require.Equal(t, []string{"42"}, h.attachDoors(), "first handshake uses the creator door")

	h.settle("42", bridge.ErrEscrowSettled)
	h.auth.InvalidateToken(first)

	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 3*time.Second, 10*time.Millisecond)

	allows := h.attachDoors()
	require.NotContains(t, allows, transport.HostRPCEscrowID)
	require.Equal(t, "99", allows[len(allows)-1], "re-Attach after session loss must land on the live escrow")

	got, err := rpc99.GetSignatures(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, map[uint32][]byte{1: []byte("sig-99")}, got)

	_, err = rpc99.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, io.Discard, nil)
	require.NoError(t, err)
}

func stealSessionOnDoor(t *testing.T, srv *httptest.Server, hostAddr, door string, signer signing.Signer) {
	t.Helper()
	base := srv.URL + transport.DefaultRoutePrefix() + "/sessions/" + door + "/rpc"
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), base)
	nonce := []byte("stolen-door-attach-nonce-01")
	ts := time.Now().Unix()
	sig, err := transport.SignAttach(signer, hostAddr, ts, signer.Address(), nonce, transport.AttachProtocolVersion, nil)
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(&rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     nonce,
		ProtocolVersion: transport.AttachProtocolVersion,
		HostAddress:     hostAddr,
		Timestamp:       ts,
		Signature:       sig,
	}))
	require.NoError(t, err)
}

func TestPeerConn_ReattachWhileLiveSessionRechecksSettledDoor(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	lookup := doorLookup{live: "99", core: doorChatCore{}}
	h := startRoutedPeerRPC(t, hostAddr, lookup)

	pc, _, rpc99 := newSharedDoorClients(t, h.srv, hostAddr, peer, "42", "99")
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	require.Equal(t, []string{"42"}, h.attachDoors())

	h.settle("42", bridge.ErrEscrowSettled)
	stealSessionOnDoor(t, h.srv, hostAddr, "99", peer)

	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 3*time.Second, 10*time.Millisecond)

	allows := h.attachDoors()
	require.Equal(t, []string{"42", "99", "42", "99"}, allows,
		"steal keeps the host session live; re-Attach on 42 must still run AllowsSender and rotate to 99")

	got, err := rpc99.GetSignatures(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, map[uint32][]byte{1: []byte("sig-99")}, got)
}

func TestPeerConn_WaitReadyOwnEscrowIsNextDoor(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	lookup := doorLookup{live: "99", core: doorChatCore{}}
	h := startRoutedPeerRPC(t, hostAddr, lookup)

	pc, _, rpc99 := newSharedDoorClients(t, h.srv, hostAddr, peer, "42", "99")
	pc.Start()
	first := append([]byte(nil), waitPeerReady(t, pc)...)
	require.Equal(t, []string{"42"}, h.attachDoors())
	require.NoError(t, rpc99.WaitReady(context.Background()))

	h.settle("42", bridge.ErrEscrowSettled)
	h.auth.InvalidateToken(first)

	require.Eventually(t, func() bool {
		tok := pc.LiveToken()
		return pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 3*time.Second, 10*time.Millisecond)

	allows := h.attachDoors()
	require.GreaterOrEqual(t, len(allows), 2)
	require.Equal(t, "99", allows[1], "WaitReady on 99 must Attach 99 first after token loss")
	require.NotContains(t, allows[1:], "42")
}

func TestPeerConn_WaitReadyFailsWhenEveryDoorSettled(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	lookup := doorLookup{live: "99", core: doorChatCore{}}
	h := startRoutedPeerRPC(t, hostAddr, lookup)

	pc, _, rpc99 := newSharedDoorClients(t, h.srv, hostAddr, peer, "42", "99")
	pc.Start()
	tok := append([]byte(nil), waitPeerReady(t, pc)...)

	h.settle("42", bridge.ErrEscrowSettled)
	h.settle("99", bridge.ErrEscrowNotFound)
	h.auth.InvalidateToken(tok)

	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		err := rpc99.WaitReady(ctx)
		return err != nil && strings.Contains(err.Error(), "no attach door")
	}, 3*time.Second, 20*time.Millisecond)

	start := time.Now()
	err := rpc99.WaitReady(context.Background())
	require.ErrorIs(t, err, transport.ErrPeerNotReady)
	require.Contains(t, err.Error(), "no attach door")
	require.Less(t, time.Since(start), 300*time.Millisecond, "exhausted doors must not park on Attach backoff")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = rpc99.Send(ctx, host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, io.Discard, nil)
	require.ErrorIs(t, err, transport.ErrPeerNotReady)
}

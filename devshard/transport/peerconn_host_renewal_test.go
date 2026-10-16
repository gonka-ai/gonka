package transport_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	devtest "devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
)

// renewalHost routes /sessions/{escrow}/rpc like versiond and counts Attach
// per URL escrow. refuse, when set, answers Attach on `_` in place of the
// handler. Allow fails `_` the way production does: it is not an escrow.
type renewalHost struct {
	srv  *httptest.Server
	auth *rpcserver.PeerAuthHandler

	mu     sync.Mutex
	refuse func() error
	host   int
	door   int
}

func startRenewalHost(t *testing.T, hostAddr string, ttl time.Duration) *renewalHost {
	t.Helper()
	h := &renewalHost{}
	h.auth = rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		SessionTTL: ttl,
		// Watch must not end the session on its own; only refresh is under test.
		Heartbeat: time.Hour,
		Allow: func(ctx context.Context, _ string) (bool, error) {
			if rpcserver.EscrowIDFromContext(ctx) == transport.HostRPCEscrowID {
				return false, bridge.ErrEscrowNotFound
			}
			return true, nil
		},
	})
	mux := rpcserver.NewMux(h.auth, rpcserver.NewSessionHandler(nil))
	ew := connect.NewErrorWriter()
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escrow, procedure, ok := cutSessionRPC(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if procedure == rpcpbconnect.PeerAuthServiceAttachProcedure {
			h.mu.Lock()
			var refuse func() error
			if escrow == transport.HostRPCEscrowID {
				h.host++
				refuse = h.refuse
			} else {
				h.door++
			}
			h.mu.Unlock()
			if refuse != nil {
				_ = ew.Write(w, r, refuse())
				return
			}
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

func (h *renewalHost) setRefuse(f func() error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refuse = f
}

func (h *renewalHost) counts() (host, door int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.host, h.door
}

func startRenewalPeer(t *testing.T, h *renewalHost, hostAddr string, peer signing.Signer) *transport.PeerConn {
	t.Helper()
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      h.srv.URL,
		RoutePrefix:  transport.DefaultRoutePrefix(),
		DoorEscrowID: "42",
		HostAddress:  hostAddr,
		Signer:       peer,
		WatchStale:   time.Minute,
		BackoffMin:   20 * time.Millisecond,
		BackoffMax:   200 * time.Millisecond,
		MinTTL:       500 * time.Millisecond,
	})
	t.Cleanup(pc.Close)
	_ = transport.NewRPCClient(transport.NewHTTPClient(h.srv.URL, "42", peer), pc,
		transport.ParseRPCEndpoints(transport.EndpointSignatures))
	pc.Start()
	return pc
}

func connectErr(code connect.Code, msg string, devshardErr string) func() error {
	return func() error {
		err := connect.NewError(code, errors.New(msg))
		if devshardErr != "" {
			err.Meta().Set(transport.HeaderDevshardError, devshardErr)
		}
		return err
	}
}

func TestPeerConn_RefreshOnHostURLWithLostTokenAttachesOnDoor(t *testing.T) {
	cases := []struct {
		name   string
		refuse func() error
	}{
		{"session expired", connectErr(connect.CodeUnauthenticated, "session expired", "")},
		{"handshake required", connectErr(connect.CodeUnauthenticated, "handshake required", "")},
		{"older child roster check on _", connectErr(connect.CodeFailedPrecondition, "escrow is not open on this host", transport.DevshardErrorEscrowNotFound)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hostAddr := devtest.MustGenerateKey(t).Address()
			peer := devtest.MustGenerateKey(t)
			h := startRenewalHost(t, hostAddr, 2*time.Second)
			pc := startRenewalPeer(t, h, hostAddr, peer)
			first := append([]byte(nil), waitPeerReady(t, pc)...)
			_, door := h.counts()
			require.Equal(t, 1, door, "first Attach is on the door")

			h.setRefuse(tc.refuse)
			require.Eventually(t, func() bool {
				host, door := h.counts()
				tok := pc.LiveToken()
				return host >= 1 && door >= 2 && pc.Ready() && len(tok) > 0 && string(tok) != string(first)
			}, 5*time.Second, 10*time.Millisecond, "a lost token on `_` must re-Attach on a real escrow")
		})
	}
}

func TestPeerConn_RefreshOnHostURLTransientErrorKeepsToken(t *testing.T) {
	cases := []struct {
		name   string
		refuse func() error
	}{
		{"unavailable", connectErr(connect.CodeUnavailable, "chain unavailable", transport.DevshardErrorChainUnavailable)},
		{"host shutting down", connectErr(connect.CodeFailedPrecondition, "host shutting down", "")},
		{"attach floor", connectErr(connect.CodeResourceExhausted, "too many attach attempts", "")},
		{"bad signature", connectErr(connect.CodeUnauthenticated, "attach timestamp outside drift window", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hostAddr := devtest.MustGenerateKey(t).Address()
			peer := devtest.MustGenerateKey(t)
			h := startRenewalHost(t, hostAddr, 4*time.Second)
			pc := startRenewalPeer(t, h, hostAddr, peer)
			first := append([]byte(nil), waitPeerReady(t, pc)...)

			h.setRefuse(tc.refuse)
			require.Eventually(t, func() bool {
				host, _ := h.counts()
				return host >= 2
			}, 6*time.Second, 5*time.Millisecond, "refresh must retry on `_`")
			_, door := h.counts()
			require.Equal(t, 1, door, "a transient refresh error must not re-Attach on the door")
			require.Equal(t, string(first), string(pc.LiveToken()), "a transient refresh error keeps the token")
			require.Equal(t, observability.PeerSessionReady, pc.State())
		})
	}
}

// The real child answers an expired renewal on `_`. Refresh is held down
// until the host stops admitting the token, the way a long outage would.
func TestPeerConn_ExpiredTokenRenewalOnHostURLRecovers(t *testing.T) {
	t.Parallel()
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	h := startRenewalHost(t, hostAddr, 2*time.Second)
	pc := startRenewalPeer(t, h, hostAddr, peer)
	first := append([]byte(nil), waitPeerReady(t, pc)...)

	h.setRefuse(connectErr(connect.CodeUnavailable, "refresh down", ""))
	require.Eventually(t, func() bool {
		_, ok := h.auth.LookupToken(first)
		return !ok
	}, 15*time.Second, 50*time.Millisecond, "the host must stop admitting the first token")
	_, door := h.counts()
	require.Equal(t, 1, door, "Unavailable must not re-Attach on the door")

	h.setRefuse(nil)
	require.Eventually(t, func() bool {
		_, door := h.counts()
		tok := pc.LiveToken()
		return door >= 2 && pc.Ready() && len(tok) > 0 && string(tok) != string(first)
	}, 5*time.Second, 10*time.Millisecond, "an expired renewal on `_` must end in a door Attach")
}

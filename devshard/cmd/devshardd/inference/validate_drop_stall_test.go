package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/chain"
	"common/httpguard"
	"connectrpc.com/connect"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
)

type dropStallPeer struct {
	srv        *httptest.Server
	rpc        *transport.RPCClient
	dropNext   atomic.Bool
	dropOnce   atomic.Bool
	refuseNext atomic.Bool
	abortNext  atomic.Bool
	abortEven  atomic.Bool
	payloads   atomic.Int32
	stallNext  atomic.Bool
	stallFor   time.Duration
	served     atomic.Int32
	attaches   atomic.Int32
	exhausted  atomic.Int32
	allowFn    func(context.Context, string) (bool, error)
}

func newDropStallPeer(t *testing.T, realIP bool, floor int) *dropStallPeer {
	httpguard.SetAllowPrivate(true)
	m := &dropStallPeer{}
	hostAddr := testutil.MustGenerateKey(t).Address()
	peer := testutil.MustGenerateKey(t)
	allow := func(ctx context.Context, e string) (bool, error) {
		m.attaches.Add(1)
		if m.allowFn != nil {
			return m.allowFn(ctx, e)
		}
		if m.stallNext.CompareAndSwap(true, false) {
			select {
			case <-time.After(m.stallFor):
			case <-ctx.Done():
				return false, ctx.Err()
			}
			return true, nil
		}
		if m.refuseNext.CompareAndSwap(true, false) {
			return false, errors.New("nope")
		}
		return true, nil
	}
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond, Allow: allow, AttachFloorPerMin: floor})
	lookup := payloadGroupLookup{}
	answer := func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		if m.abortNext.CompareAndSwap(true, false) {
			return nil, connect.NewError(connect.CodeAborted, errors.New("busy"))
		}
		m.served.Add(1)
		return &rpcpb.GetPayloadResponse{}, nil
	}
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(lookup), rpcserver.WithPayloadService(rpcserver.NewPayloadHandler(lookup, answer)))
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if realIP {
			r.Header.Set("X-Real-IP", "10.1.2.3")
		}
		if strings.Contains(r.URL.Path, "GetPayload") && m.abortEven.Load() && m.payloads.Add(1)%2 == 1 {
			m.abortNext.Store(true)
		} else if strings.Contains(r.URL.Path, "GetPayload") && (m.dropNext.Load() || m.dropOnce.CompareAndSwap(true, false)) {
			if m.stallFor > 0 {
				m.stallNext.Store(true)
			} else {
				m.refuseNext.Store(true)
			}
			go m.srv.CloseClientConnections()
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
				}
				return
			}
		}
		rw := &dropStallSniff{ResponseWriter: w}
		mux.ServeHTTP(rw, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
		if strings.Contains(r.URL.Path, "Attach") && rw.code == http.StatusTooManyRequests {
			m.exhausted.Add(1)
		}
	}))
	t.Cleanup(m.srv.Close)
	t.Cleanup(auth.Close)
	pc := transport.NewPeerConn(transport.PeerConnConfig{BaseURL: m.srv.URL, HostAddress: hostAddr, Signer: peer, DirectMux: true, DoorEscrowID: "escrow-1", WatchStale: time.Minute})
	t.Cleanup(pc.Close)
	pc.Start()
	m.rpc = transport.NewRPCClient(transport.NewHTTPClient(m.srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointPayload))
	return m
}

type dropStallSniff struct {
	http.ResponseWriter
	code int
}

func (c *dropStallSniff) WriteHeader(code int) { c.code = code; c.ResponseWriter.WriteHeader(code) }
func (c *dropStallSniff) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func dropStallVerdict(t *testing.T, rpc *transport.RPCClient, id string) (error, bool) {
	t.Helper()
	ctx := context.Background()
	_, err := fetchSignedPayloads(ctx, nil, rpc, "http://unused", "path", id, "val", time.Now().UnixNano(), 10, "sig", 0)
	err = classifyPayloadFetchError(ctx, err)
	phase := new(chain.Phase)
	phase.SetEpoch(10)
	v := executorFaultVerdict(ctx, phase, faultReq(10), 10, err, true)
	return err, v != nil && !v.Valid
}

// An executor that drops every GetPayload and holds the re-Attach past the
// retry wait is silent for the whole header budget: Valid:false.
func TestFetchPayloads_DropThenStalledReattachVotesFalse(t *testing.T) {
	m := newDropStallPeer(t, false, 0)
	m.stallFor = 6 * time.Second
	if _, voted := dropStallVerdict(t, m.rpc, "1"); voted {
		t.Fatal("warm fetch voted")
	}
	m.dropNext.Store(true)
	err, voted := dropStallVerdict(t, m.rpc, "42")
	if !voted {
		t.Fatalf("no Valid:false after drop + stalled re-Attach: %v", err)
	}
}

// One dropped call followed by a re-Attach inside the budget is served.
func TestFetchPayloads_DropOnceThenReattachServes(t *testing.T) {
	for _, stall := range []time.Duration{time.Second, 6 * time.Second} {
		m := newDropStallPeer(t, false, 0)
		m.stallFor = stall
		dropStallVerdict(t, m.rpc, "1")
		m.dropOnce.Store(true)
		err, voted := dropStallVerdict(t, m.rpc, "42")
		if err != nil || voted {
			t.Fatalf("stall %v: err=%v voted=%v", stall, err, voted)
		}
		if got := m.served.Load(); got != 2 {
			t.Fatalf("stall %v: served %d, want 2", stall, got)
		}
	}
}

// Each GetPayload alternates an error answer with a dropped call and a held
// re-Attach, so every fetch ends in a bare not-ready with budget left.
func TestFetchPayloads_AbortThenDropStalledVotesFalse(t *testing.T) {
	m := newDropStallPeer(t, false, 0)
	m.stallFor = 6 * time.Second
	if _, voted := dropStallVerdict(t, m.rpc, "1"); voted {
		t.Fatal("warm fetch voted")
	}
	m.abortEven.Store(true)
	m.dropNext.Store(true)
	err, voted := dropStallVerdict(t, m.rpc, "42")
	if !voted {
		t.Fatalf("no Valid:false after abort + drop + stalled re-Attach: %v", err)
	}
}

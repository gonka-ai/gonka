package inference

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chain"
	"common/httpguard"
	commonvalidation "common/validation"
	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
)

type escrowViewBridge struct {
	bridge.MainnetBridge
	info *bridge.EscrowInfo
	err  error
}

func (b escrowViewBridge) GetEscrow(string) (*bridge.EscrowInfo, error) { return b.info, b.err }

var openEscrow = escrowViewBridge{info: &bridge.EscrowInfo{EscrowID: "escrow-1"}}

// doorClient is a validator RPCClient whose executor answers Attach with allow.
// served counts GetPayload calls that reached the executor.
func doorClient(t *testing.T, allow rpcserver.AllowPeer) (*transport.RPCClient, *atomic.Int32) {
	rpc, served, _ := doorExecutor(t, allow, false, false)
	return rpc, served
}

func doorClientDropping(t *testing.T, allow rpcserver.AllowPeer, drop bool) (*transport.RPCClient, *atomic.Int32) {
	rpc, served, _ := doorExecutor(t, allow, drop, false)
	return rpc, served
}

// doorExecutor can also drop every connection on GetPayload instead of
// answering it, or answer it with an empty payload (serve).
func doorExecutor(t *testing.T, allow rpcserver.AllowPeer, drop, serve bool) (*transport.RPCClient, *atomic.Int32, *httptest.Server) {
	t.Helper()
	httpguard.SetAllowPrivate(true)
	hostAddr := testutil.MustGenerateKey(t).Address()
	peer := testutil.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond, Allow: allow})
	lookup := payloadGroupLookup{}
	served := new(atomic.Int32)
	answer := func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		served.Add(1)
		if serve {
			return &rpcpb.GetPayloadResponse{}, nil
		}
		return nil, errors.New("not under test")
	}
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(lookup), rpcserver.WithPayloadService(rpcserver.NewPayloadHandler(lookup, answer)))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if drop && strings.Contains(r.URL.Path, "GetPayload") {
			go srv.CloseClientConnections()
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					c.Close()
				}
				return
			}
		}
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	pc := transport.NewPeerConn(transport.PeerConnConfig{BaseURL: srv.URL, HostAddress: hostAddr, Signer: peer, DirectMux: true, DoorEscrowID: "escrow-1", WatchStale: time.Minute})
	t.Cleanup(pc.Close)
	pc.Start()
	return transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointPayload)), served, srv
}

const testBudget = 400 * time.Millisecond

// doorTimings shortens the header budget, the chain check and the re-Attach gap.
func doorTimings(t *testing.T) {
	t.Helper()
	oldHeader, oldCheck, oldGap := payloadFetchHeaderTimeout, doorRefusalCheckTimeout, doorRefusalRetryGap
	payloadFetchHeaderTimeout, doorRefusalCheckTimeout, doorRefusalRetryGap = testBudget, time.Second, 20*time.Millisecond
	t.Cleanup(func() {
		payloadFetchHeaderTimeout, doorRefusalCheckTimeout, doorRefusalRetryGap = oldHeader, oldCheck, oldGap
	})
}

// doorVerdict runs one fetch the way fetchPayloadsFromExecutor does and
// reports whether executorFaultVerdict votes Valid:false.
func doorVerdict(t *testing.T, rpc *transport.RPCClient, br bridge.MainnetBridge) (error, bool) {
	t.Helper()
	ctx := context.Background()
	_, err := fetchPastRefusedDoor(ctx, br, rpc, "escrow-1", func(ctx context.Context) (*commonvalidation.PayloadResponse, error) {
		return fetchSignedPayloads(ctx, nil, rpc, "http://unused", "path", "42", "val", time.Now().UnixNano(), 10, "sig", 0)
	})
	err = classifyPayloadFetchError(ctx, err)
	phase := new(chain.Phase)
	phase.SetEpoch(10)
	v := executorFaultVerdict(ctx, phase, faultReq(10), 10, err, true)
	return err, v != nil && !v.Valid
}

func always(err error) rpcserver.AllowPeer {
	return func(context.Context, string) (bool, error) { return false, err }
}

// failingFor fails every Attach with err until d after the first one, like
// HostManager's resolution-failure cache, then admits.
func failingFor(err error, d time.Duration) rpcserver.AllowPeer {
	var first atomic.Int64
	return func(context.Context, string) (bool, error) {
		now := time.Now().UnixNano()
		first.CompareAndSwap(0, now)
		if now-first.Load() < int64(d) {
			return false, err
		}
		return true, nil
	}
}

func TestFetchPastRefusedDoor_ExecutorLies_VotesFalse(t *testing.T) {
	for name, cause := range map[string]error{
		"settled":   bridge.ErrEscrowSettled,
		"not_found": bridge.ErrEscrowNotFound,
		"uncoded":   errors.New("nope"), // "escrow is not open on this host"
	} {
		t.Run(name, func(t *testing.T) {
			doorTimings(t)
			rpc, served := doorClient(t, always(cause))
			start := time.Now()
			err, voted := doorVerdict(t, rpc, openEscrow)
			require.ErrorIs(t, err, errExecutorPayloadFault)
			require.True(t, voted)
			require.Zero(t, served.Load())
			require.GreaterOrEqual(t, time.Since(start), testBudget)
		})
	}
}

// The executor admits every other Attach and drops the session on
// GetPayload, so each fetch ends on a refused re-Attach.
func TestFetchPastRefusedDoor_AlternatingExecutor_VotesFalse(t *testing.T) {
	doorTimings(t)
	var calls atomic.Int32
	rpc, _ := doorClientDropping(t, func(context.Context, string) (bool, error) {
		if calls.Add(1)%2 == 1 {
			return false, errors.New("nope")
		}
		return true, nil
	}, true)
	_, voted := doorVerdict(t, rpc, openEscrow)
	require.True(t, voted)
}

func TestFetchPastRefusedDoor_ChainSaysSettled_NoVote(t *testing.T) {
	doorTimings(t)
	rpc, _ := doorClient(t, always(bridge.ErrEscrowSettled))
	start := time.Now()
	err, voted := doorVerdict(t, rpc, escrowViewBridge{info: &bridge.EscrowInfo{EscrowID: "escrow-1", Settled: true}})
	require.ErrorIs(t, err, transport.ErrPeerNotReady)
	require.NotErrorIs(t, err, errExecutorPayloadFault)
	require.False(t, voted)
	require.Less(t, time.Since(start), testBudget)
}

func TestFetchPastRefusedDoor_ChainUnknown_NoVote(t *testing.T) {
	doorTimings(t)
	rpc, _ := doorClient(t, always(bridge.ErrEscrowSettled))
	_, voted := doorVerdict(t, rpc, escrowViewBridge{err: errors.New("chain down")})
	require.False(t, voted)
	_, voted = doorVerdict(t, rpc, nil)
	require.False(t, voted)
}

// An honest executor whose refusal ends within the budget (chain lag, a
// cached store error) is fetched normally in the same attempt.
func TestFetchPastRefusedDoor_ShortHonestRefusal_Fetches(t *testing.T) {
	for name, cause := range map[string]error{
		"not_found":   bridge.ErrEscrowNotFound,
		"store_error": errors.New("get session meta: dial tcp: connection refused"),
	} {
		t.Run(name, func(t *testing.T) {
			doorTimings(t)
			rpc, served, _ := doorExecutor(t, failingFor(cause, testBudget/2), false, true)
			_, voted := doorVerdict(t, rpc, openEscrow)
			require.False(t, voted)
			require.NotZero(t, served.Load())
			require.False(t, rpc.DoorRefused())
		})
	}
}

// A refusal longer than the budget is voted false like a silent executor.
func TestFetchPastRefusedDoor_RefusalOutlastsBudget_VotesFalse(t *testing.T) {
	doorTimings(t)
	rpc, _ := doorClient(t, failingFor(bridge.ErrEscrowNotFound, 3*testBudget))
	_, voted := doorVerdict(t, rpc, openEscrow)
	require.True(t, voted)
}

func TestFetchPastRefusedDoor_SilentExecutor_StillVotesFalse(t *testing.T) {
	doorTimings(t)
	rpc, _ := doorClient(t, func(ctx context.Context, _ string) (bool, error) { <-ctx.Done(); return false, ctx.Err() })
	_, voted := doorVerdict(t, rpc, openEscrow)
	require.False(t, rpc.DoorRefused())
	require.True(t, voted)
}

// Concurrent validations of one escrow share the door; one fetch's revive
// must not let another leave without a verdict.
func TestFetchPastRefusedDoor_Concurrent(t *testing.T) {
	run := func(t *testing.T, rpc *transport.RPCClient, n int) int32 {
		var wg sync.WaitGroup
		var votes atomic.Int32
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, voted := doorVerdict(t, rpc, openEscrow); voted {
					votes.Add(1)
				}
			}()
		}
		wg.Wait()
		return votes.Load()
	}
	t.Run("liar", func(t *testing.T) {
		doorTimings(t)
		rpc, _ := doorClient(t, always(errors.New("nope")))
		require.EqualValues(t, 8, run(t, rpc, 8))
	})
	t.Run("short_honest", func(t *testing.T) {
		doorTimings(t)
		rpc, _, _ := doorExecutor(t, failingFor(bridge.ErrEscrowNotFound, testBudget/2), false, true)
		require.Zero(t, run(t, rpc, 8))
	})
}

// A settle this host's node saw only after the first check is not voted.
func TestFetchPastRefusedDoor_SettleSeenLate_NoVote(t *testing.T) {
	doorTimings(t)
	rpc, _ := doorClient(t, always(bridge.ErrEscrowSettled))
	_, voted := doorVerdict(t, rpc, &lateSettle{})
	require.False(t, voted)
}

type lateSettle struct {
	bridge.MainnetBridge
	calls atomic.Int32
}

func (b *lateSettle) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return &bridge.EscrowInfo{EscrowID: "escrow-1", Settled: b.calls.Add(1) > 1}, nil
}

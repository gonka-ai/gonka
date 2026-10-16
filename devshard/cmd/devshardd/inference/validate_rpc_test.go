package inference

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"common/chain"
	"common/httpguard"
	commonvalidation "common/validation"
	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
	"devshard/types"
)

func TestFetchSignedPayloads_RPCWhenOptedIn(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	var httpHit, rpcHit atomic.Bool
	rpc := newPayloadRPCClient(t, func(_ context.Context, _ rpcserver.SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		rpcHit.Store(true)
		require.Equal(t, "42", req.GetInferenceId())
		require.Equal(t, "dGVzdC1zaWc=", string(req.GetSignature()))
		return &rpcpb.GetPayloadResponse{
			InferenceId:     req.GetInferenceId(),
			PromptPayload:   []byte("prompt"),
			ResponsePayload: []byte("response"),
		}, nil
	})
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		httpHit.Store(true)
		return nil, errors.New("http GET /payloads must not run when payload is opted in")
	})}

	resp, err := fetchSignedPayloads(context.Background(), httpClient, rpc, "http://unused", "path",
		"42", "val", 1, 10, "dGVzdC1zaWc=", 0)
	require.NoError(t, err)
	require.True(t, rpcHit.Load())
	require.False(t, httpHit.Load())
	require.Equal(t, []byte("prompt"), resp.PromptPayload)
	require.Equal(t, []byte("response"), resp.ResponsePayload)
}

func TestFetchSignedPayloads_NoHTTPFallback(t *testing.T) {
	var httpHit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHit.Store(true)
		w.WriteHeader(http.StatusGone)
	}))
	t.Cleanup(srv.Close)

	t.Run("nil client", func(t *testing.T) {
		httpHit.Store(false)
		_, err := fetchSignedPayloads(context.Background(), srv.Client(), nil, srv.URL, "",
			"42", "val", 1, 10, "sig", 0)
		require.ErrorIs(t, err, errPayloadRPCUnavailable)
		require.False(t, httpHit.Load())
	})
	t.Run("endpoints without payload", func(t *testing.T) {
		httpHit.Store(false)
		peer := testutil.MustGenerateKey(t)
		rpc := transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), nil, transport.ParseRPCEndpoints(transport.EndpointGossip))
		_, err := fetchSignedPayloads(context.Background(), srv.Client(), rpc, srv.URL, "",
			"42", "val", 1, 10, "sig", 0)
		require.ErrorIs(t, err, errPayloadRPCUnavailable)
		require.False(t, httpHit.Load())
	})
}

func TestFetchSignedPayloads_RPCNotFoundIsGone(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("pruned"))
	})
	_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
		"42", "val", 1, 10, "sig", 0)
	require.ErrorIs(t, err, commonvalidation.ErrPayloadGone)
}

func TestPayloadResponseFromRPC_RateLimitIsNotTooLarge(t *testing.T) {
	_, err := payloadResponseFromRPC(nil, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))
	require.ErrorIs(t, err, ErrPayloadRateLimited)
	require.NotErrorIs(t, err, commonvalidation.ErrPayloadTooLarge)
	classified := classifyPayloadFetchError(context.Background(), err)
	require.ErrorIs(t, classified, ErrPayloadRateLimited)
	require.NotErrorIs(t, classified, errExecutorPayloadFault)
	require.NotErrorIs(t, classified, commonvalidation.ErrPayloadTooLarge)

	limited := connect.NewError(connect.CodeResourceExhausted, errors.New("too many escrow lookups"))
	limited.Meta().Set(transport.HeaderDevshardError, transport.DevshardErrorEscrowLookupLimited)
	_, err = payloadResponseFromRPC(nil, limited)
	require.ErrorIs(t, err, ErrPayloadRateLimited)
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), err), errExecutorPayloadFault)

	_, tooBig := payloadResponseFromRPC(nil, connect.NewError(connect.CodeResourceExhausted, errors.New("message size 99 is larger than configured max 10")))
	require.ErrorIs(t, tooBig, commonvalidation.ErrPayloadTooLarge)
	require.NotErrorIs(t, tooBig, ErrPayloadRateLimited)
}

func TestPayloadRPCFetchDone(t *testing.T) {
	require.True(t, payloadRPCFetchDone(nil))
	require.True(t, payloadRPCFetchDone(fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)))
	require.True(t, payloadRPCFetchDone(fmt.Errorf("%w: rpc read cap", commonvalidation.ErrPayloadTooLarge)))
	require.True(t, payloadRPCFetchDone(connect.NewError(connect.CodeUnavailable, errors.New("connection refused"))))
	require.False(t, payloadRPCFetchDone(connect.NewError(connect.CodeInternal, errors.New("testenv payload fault"))))
	require.True(t, payloadRPCFetchDone(fmt.Errorf("%w: rate limit exceeded", ErrPayloadRateLimited)))
	limited := connect.NewError(connect.CodeResourceExhausted, errors.New("too many escrow lookups"))
	limited.Meta().Set(transport.HeaderDevshardError, transport.DevshardErrorEscrowLookupLimited)
	_, wrapped := payloadResponseFromRPC(nil, limited)
	require.True(t, payloadRPCFetchDone(wrapped), "escrow_lookup_limited must not take the short payload retry")
}

func setPayloadRateLimitRetryDelay(t *testing.T, delay, maxDelay time.Duration) {
	t.Helper()
	prevDelay, prevLookup, prevMax := payloadRateLimitRetryDelay, payloadEscrowLookupRetryDelay, payloadRateLimitRetryMaxDelay
	payloadRateLimitRetryDelay, payloadEscrowLookupRetryDelay, payloadRateLimitRetryMaxDelay = delay, maxDelay, maxDelay
	t.Cleanup(func() {
		payloadRateLimitRetryDelay, payloadEscrowLookupRetryDelay, payloadRateLimitRetryMaxDelay = prevDelay, prevLookup, prevMax
	})
}

// rateLimitedRPC is a 429 whose Retry-After exceeds rpcRetry's 5s budget, so
// GetPayload returns it at once instead of retrying inside the transport.
func rateLimitedRPC() error {
	ce := connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))
	ce.Meta().Set("Retry-After", "10")
	return ce
}

func TestFetchPayloadsWithRateLimitRetry_RPC(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	setPayloadRateLimitRetryDelay(t, 10*time.Millisecond, 50*time.Millisecond)

	t.Run("429 then success fetches again with a new signature", func(t *testing.T) {
		var served atomic.Int32
		rpc := newPayloadRPCClient(t, func(_ context.Context, _ rpcserver.SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			if served.Add(1) == 1 {
				return nil, rateLimitedRPC()
			}
			return &rpcpb.GetPayloadResponse{InferenceId: req.GetInferenceId(), ResponsePayload: []byte("response")}, nil
		})
		var signatures []string
		resp, err := fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), func(ctx context.Context) (*commonvalidation.PayloadResponse, error) {
			sig := fmt.Sprintf("sig-%d", len(signatures))
			signatures = append(signatures, sig)
			return fetchSignedPayloads(ctx, nil, rpc, "http://unused", "path", "42", "val", 1, 10, sig, 0)
		})
		require.NoError(t, err)
		require.Equal(t, []byte("response"), resp.ResponsePayload)
		require.Equal(t, int32(2), served.Load())
		require.Equal(t, []string{"sig-0", "sig-1"}, signatures)
	})

	t.Run("429 twice votes false after one retry", func(t *testing.T) {
		var served atomic.Int32
		rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			served.Add(1)
			return nil, rateLimitedRPC()
		})
		_, err := fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), func(ctx context.Context) (*commonvalidation.PayloadResponse, error) {
			return fetchSignedPayloads(ctx, nil, rpc, "http://unused", "path", "42", "val", 1, 10, "sig", 0)
		})
		require.ErrorIs(t, err, ErrPayloadRateLimited)
		require.ErrorIs(t, err, errPayloadRateLimitRetried)
		require.Equal(t, int32(2), served.Load(), "a 429 must skip the short retry; the only retry is after the wait")

		classified := classifyPayloadFetchError(context.Background(), err)
		require.ErrorIs(t, classified, errExecutorPayloadFault)
		phase := new(chain.Phase)
		phase.SetEpoch(10)
		verdict := executorFaultVerdict(context.Background(), phase, faultReq(10), 10, classified, true)
		require.NotNil(t, verdict)
		require.False(t, verdict.Valid)
		require.Contains(t, fmt.Sprint(verdict.Details), string(observability.ReasonRateLimited))
	})
}

func TestFetchPayloadsWithRateLimitRetry_WaitsBeforeRetry(t *testing.T) {
	setPayloadRateLimitRetryDelay(t, 50*time.Millisecond, time.Second)
	var calls []time.Time
	_, err := fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), func(context.Context) (*commonvalidation.PayloadResponse, error) {
		calls = append(calls, time.Now())
		return nil, fmt.Errorf("get payload: %w", ErrPayloadRateLimited)
	})
	require.ErrorIs(t, err, errPayloadRateLimitRetried)
	require.Len(t, calls, 2)
	require.GreaterOrEqual(t, calls[1].Sub(calls[0]), 50*time.Millisecond)
}

func TestFetchPayloadsWithRateLimitRetry_OtherErrorsDoNotWait(t *testing.T) {
	setPayloadRateLimitRetryDelay(t, time.Hour, time.Hour)
	calls := 0
	_, err := fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), func(context.Context) (*commonvalidation.PayloadResponse, error) {
		calls++
		return nil, errors.New("executor returned status 500")
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPayloadRateLimited)
	require.Equal(t, 1, calls)
}

func TestFetchPayloadsWithRateLimitRetry_StopDuringWaitDoesNotVote(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	setPayloadRateLimitRetryDelay(t, time.Hour, time.Hour)
	rateLimited := func(context.Context) (*commonvalidation.PayloadResponse, error) {
		return nil, fmt.Errorf("get payload: %w", ErrPayloadRateLimited)
	}

	t.Run("release of outbound peers", func(t *testing.T) {
		t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
		time.AfterFunc(20*time.Millisecond, transport.ReleaseOutboundPeerConns)
		start := time.Now()
		_, err := fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), rateLimited)
		require.Less(t, time.Since(start), 2*time.Second)
		require.Error(t, err)
		require.NotErrorIs(t, err, errPayloadRateLimitRetried)
		require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), err), errExecutorPayloadFault)
	})

	t.Run("cancelled caller", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		_, err := fetchPayloadsWithRateLimitRetry(ctx, faultReq(10), rateLimited)
		require.ErrorIs(t, err, context.Canceled)
		require.NotErrorIs(t, classifyPayloadFetchError(ctx, err), errExecutorPayloadFault)
	})
}

func TestPayloadRateLimitWait(t *testing.T) {
	setPayloadRateLimitRetryDelay(t, 30*time.Second, 60*time.Second)
	withRetryAfter := func(v string) error {
		ce := connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))
		if v != "" {
			ce.Meta().Set("Retry-After", v)
		}
		return fmt.Errorf("%w: %w", ErrPayloadRateLimited, ce)
	}
	require.Equal(t, 30*time.Second, payloadRateLimitWait(withRetryAfter("")))
	require.Equal(t, 30*time.Second, payloadRateLimitWait(withRetryAfter("1")))
	require.Equal(t, 45*time.Second, payloadRateLimitWait(withRetryAfter("45")))
	require.Equal(t, 60*time.Second, payloadRateLimitWait(withRetryAfter("600")))

	lookup := connect.NewError(connect.CodeResourceExhausted, errors.New("too many escrow lookups"))
	lookup.Meta().Set(transport.HeaderDevshardError, transport.DevshardErrorEscrowLookupLimited)
	_, lookupErr := payloadResponseFromRPC(nil, lookup)
	require.Equal(t, time.Minute, payloadRateLimitWait(lookupErr), "the lookup budget is a sliding minute and sends no Retry-After")
}

// A deferral used to expire, so a 429 that arrived after the grace window was
// forgiven and an executor could answer 429 forever. There is no record and
// no window: a later validation still votes false.
func TestFetchPayloadsWithRateLimitRetry_LaterAttemptStillVotesFalse(t *testing.T) {
	setPayloadRateLimitRetryDelay(t, time.Millisecond, time.Millisecond)
	phase := new(chain.Phase)
	phase.SetEpoch(10)
	req := faultReq(10)
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := fetchPayloadsWithRateLimitRetry(context.Background(), req, func(context.Context) (*commonvalidation.PayloadResponse, error) {
			return nil, fmt.Errorf("get payload: %w", ErrPayloadRateLimited)
		})
		require.ErrorIs(t, err, errPayloadRateLimitRetried, "attempt %d", attempt)
		classified := classifyPayloadFetchError(context.Background(), err)
		require.ErrorIs(t, classified, errExecutorPayloadFault, "attempt %d", attempt)
		verdict := executorFaultVerdict(context.Background(), phase, req, req.EpochID, classified, true)
		require.NotNil(t, verdict, "attempt %d", attempt)
		require.False(t, verdict.Valid)
	}
}

func TestFetchPayloadsWithRateLimitRetry_SuccessLeavesNoFault(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	setPayloadRateLimitRetryDelay(t, time.Millisecond, time.Millisecond)

	var served atomic.Int32
	rpc := newPayloadRPCClient(t, func(_ context.Context, _ rpcserver.SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		n := served.Add(1)
		if n == 1 || n == 3 {
			return nil, rateLimitedRPC()
		}
		return &rpcpb.GetPayloadResponse{InferenceId: req.GetInferenceId(), ResponsePayload: []byte("response")}, nil
	})
	fetch := func(ctx context.Context) (*commonvalidation.PayloadResponse, error) {
		return fetchSignedPayloads(ctx, nil, rpc, "http://unused", "path", "42", "val", 1, 10, "sig", 0)
	}

	resp, err := fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), fetch)
	require.NoError(t, err)
	require.Equal(t, []byte("response"), resp.ResponsePayload)
	require.Equal(t, int32(2), served.Load())

	resp, err = fetchPayloadsWithRateLimitRetry(context.Background(), faultReq(10), fetch)
	require.NoError(t, err)
	require.Equal(t, []byte("response"), resp.ResponsePayload)
	require.Equal(t, int32(4), served.Load(), "a successful retry must not leave a mark that skips or forces the next retry")
}

func TestFetchSignedPayloads_RateLimitSkipsShortRetry(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 5 * time.Second
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	var served atomic.Int32
	rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		served.Add(1)
		return nil, rateLimitedRPC()
	})
	start := time.Now()
	_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
		"42", "val", 1, 10, "sig", 0)
	require.Less(t, time.Since(start), time.Second, "the 500ms payload retry must not run for a 429")
	require.ErrorIs(t, err, ErrPayloadRateLimited)
	require.NotErrorIs(t, err, errPayloadRateLimitRetried)
	require.Equal(t, int32(1), served.Load())
	require.NotErrorIs(t, classifyPayloadFetchError(context.Background(), err), errExecutorPayloadFault)
}

func TestFetchSignedPayloads_RPCRetriesInternal(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	prev := payloadFetchRetryBackoff
	payloadFetchRetryBackoff = 0
	t.Cleanup(func() { payloadFetchRetryBackoff = prev })

	t.Run("internal retries twice", func(t *testing.T) {
		var n atomic.Int32
		rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			n.Add(1)
			return nil, connect.NewError(connect.CodeInternal, errors.New("testenv payload fault"))
		})
		_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
			"42", "val", 1, 10, "sig", 0)
		require.Error(t, err)
		require.False(t, errors.Is(err, commonvalidation.ErrPayloadGone))
		require.Equal(t, int32(payloadFetchAttempts), n.Load())
	})

	t.Run("success on second attempt", func(t *testing.T) {
		var n atomic.Int32
		rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			if n.Add(1) == 1 {
				return nil, connect.NewError(connect.CodeInternal, errors.New("testenv payload fault"))
			}
			return &rpcpb.GetPayloadResponse{
				InferenceId:     "42",
				PromptPayload:   []byte("prompt"),
				ResponsePayload: []byte("response"),
			}, nil
		})
		resp, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
			"42", "val", 1, 10, "sig", 0)
		require.NoError(t, err)
		require.Equal(t, []byte("response"), resp.ResponsePayload)
		require.Equal(t, int32(2), n.Load())
	})
}

func TestFetchSignedPayloads_RPCRespectsMaxBytes(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	t.Run("over cap is too large", func(t *testing.T) {
		var n atomic.Int32
		body := bytes.Repeat([]byte("x"), 2048)
		rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			n.Add(1)
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		_, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
			"42", "val", 1, 10, "sig", 512)
		require.ErrorIs(t, err, commonvalidation.ErrPayloadTooLarge)
		require.Equal(t, int32(1), n.Load())
	})
	t.Run("within cap succeeds", func(t *testing.T) {
		body := []byte("response")
		rpc := newPayloadRPCClient(t, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
		})
		resp, err := fetchSignedPayloads(context.Background(), nil, rpc, "http://unused", "path",
			"42", "val", 1, 10, "sig", 1024)
		require.NoError(t, err)
		require.Equal(t, body, resp.ResponsePayload)
	})
}

func TestValidator_PayloadRPCRequiresPayloadEndpoint(t *testing.T) {
	v := NewValidator(nil, nil, nil, nil, "v1", nil, nil, nil, false)
	require.Nil(t, v.payloadRPC("http://example", "addr", "escrow-1"))

	signer := testutil.MustGenerateKey(t)
	v.SetPayloadRPC(signer, transport.ParseRPCEndpoints(transport.EndpointGossip))
	require.Nil(t, v.payloadRPC("http://example", "addr", "escrow-1"))

	v.SetPayloadRPC(signer, transport.ParseRPCEndpoints(transport.EndpointPayload))
	require.Nil(t, v.payloadRPC("", "addr", "escrow-1"))
}

func newPayloadRPCClient(t *testing.T, serve rpcserver.GetPayloadFunc) *transport.RPCClient {
	t.Helper()
	hostAddr := testutil.MustGenerateKey(t).Address()
	peer := testutil.MustGenerateKey(t)
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond})
	lookup := payloadGroupLookup{}
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(lookup), rpcserver.WithPayloadService(rpcserver.NewPayloadHandler(lookup, serve)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(rpcserver.WithEscrowID(r.Context(), "escrow-1")))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)

	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      srv.URL,
		HostAddress:  hostAddr,
		Signer:       peer,
		DirectMux:    true,
		DoorEscrowID: "escrow-1",
		WatchStale:   time.Minute,
	})
	t.Cleanup(pc.Close)
	pc.Start()
	require.Eventually(t, pc.Ready, 3*time.Second, 10*time.Millisecond)
	return transport.NewRPCClient(transport.NewHTTPClient(srv.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointPayload))
}

type payloadGroupLookup struct{}

func (payloadGroupLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return payloadGroupCore{}, nil
}

func (payloadGroupLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return payloadGroupLookup{}.SessionServerExisting(id)
}

func (payloadGroupLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return payloadGroupLookup{}.SessionServerExisting(id)
}

func (payloadGroupLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
	return payloadGroupLookup{}.SessionForParticipant(id, addr)
}

type payloadGroupCore struct{}

func (payloadGroupCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) { return nil, nil }
func (payloadGroupCore) AllowsSender(string) bool                             { return true }
func (payloadGroupCore) IsGroupMember(string) bool                            { return true }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

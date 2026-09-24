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

	"common/httpguard"
	commonvalidation "common/validation"
	"devshard/internal/testutil"
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

func TestPayloadRPCFetchDone(t *testing.T) {
	require.True(t, payloadRPCFetchDone(nil))
	require.True(t, payloadRPCFetchDone(fmt.Errorf("payload not found: %w", commonvalidation.ErrPayloadGone)))
	require.True(t, payloadRPCFetchDone(fmt.Errorf("%w: rpc read cap", commonvalidation.ErrPayloadTooLarge)))
	require.True(t, payloadRPCFetchDone(connect.NewError(connect.CodeUnavailable, errors.New("connection refused"))))
	require.False(t, payloadRPCFetchDone(connect.NewError(connect.CodeInternal, errors.New("testenv payload fault"))))
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
	v := NewValidator(nil, nil, nil, nil, "v1", nil, nil, false)
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

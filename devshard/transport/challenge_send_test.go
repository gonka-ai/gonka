package transport_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"common/httpguard"
	"devshard/host"
	devtest "devshard/internal/testutil"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
	"devshard/types"
)

// challengeSendCore answers ChallengeReceipt through serve.
type challengeSendCore struct {
	retryRPCCore
	serve func(transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error)
}

func (c challengeSendCore) ServeChallengeReceipt(_ context.Context, req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	return c.serve(req)
}

type challengeSendLookup struct{ core challengeSendCore }

func (l challengeSendLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return l.core, nil
}
func (l challengeSendLookup) SessionForParticipant(id, _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}
func (l challengeSendLookup) SessionForOwner(id, _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}
func (l challengeSendLookup) SessionForStartProof(id, _ string, _ func() ([]types.Diff, error), _ string) (rpcserver.SessionCore, error) {
	return l.SessionServerExisting(id)
}

func newChallengeSendClient(t *testing.T, serve func(transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error)) *transport.RPCClient {
	t.Helper()
	rpc := newLargeRPCClient(t, challengeSendLookup{core: challengeSendCore{serve: serve}}, transport.EndpointChallengeReceipt, transport.DefaultClientConfig())
	t.Cleanup(rpc.Close)
	return rpc
}

const challengeSendBlob = 64 << 10

func challengeSendWire(blob []byte) types.DiffWire {
	return types.DiffWire{
		Nonce:         1,
		Txs:           blob,
		UserSig:       bytes.Repeat([]byte{1}, 65),
		PostStateRoot: bytes.Repeat([]byte{2}, 32),
	}
}

// prepareTrackedPage prepares a one-diff page and reports whether the page's
// stored bytes are still reachable. Nothing outside the send keeps them.
func prepareTrackedPage(t *testing.T, rpc *transport.RPCClient, reload host.WireReload) (func(context.Context) error, func() bool) {
	t.Helper()
	blob := new([challengeSendBlob]byte)
	for i := range blob {
		blob[i] = 0xab
	}
	alive := weak.Make(blob)
	send, err := rpc.PrepareChallengeCatchUp(7, []types.DiffWire{challengeSendWire(blob[:])}, reload, "")
	require.NoError(t, err)
	return send, func() bool { return alive.Value() != nil }
}

func TestChallengeCatchUpDropsThePageInFlight(t *testing.T) {
	entered := make(chan transport.ChallengeReceiptRequest, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	rpc := newChallengeSendClient(t, func(req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
		entered <- req
		<-release
		return &transport.ChallengeReceiptResponse{}, nil
	})
	// Registered after the server, so it runs first and Close does not wait
	// on a blocked handler.
	t.Cleanup(unblock)

	var reloads atomic.Int32
	send, alive := prepareTrackedPage(t, rpc, func() ([]types.DiffWire, error) {
		reloads.Add(1)
		return nil, errors.New("reload not expected")
	})
	done := make(chan error, 1)
	go func() { done <- send(context.Background()) }()

	var req transport.ChallengeReceiptRequest
	select {
	case req = <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("executor never received the page")
	}
	require.Equal(t, uint64(7), req.InferenceID)
	require.Len(t, req.Diffs, 1)
	require.Equal(t, transport.DiffWireTxs(1, bytes.Repeat([]byte{0xab}, challengeSendBlob)), req.Diffs[0].Txs)

	require.Eventually(t, func() bool {
		runtime.GC()
		return !alive()
	}, 5*time.Second, 10*time.Millisecond, "the page stays reachable while the executor applies it")

	unblock()
	require.NoError(t, <-done)
	require.Zero(t, reloads.Load())
}

func TestChallengeCatchUpRetryReloadsThePage(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	rpc := newChallengeSendClient(t, func(req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, req.Diffs[0].Txs)
		if len(bodies) == 1 {
			return nil, storage.ErrStorageBusy
		}
		return &transport.ChallengeReceiptResponse{}, nil
	})

	var reloads atomic.Int32
	send, _ := prepareTrackedPage(t, rpc, func() ([]types.DiffWire, error) {
		reloads.Add(1)
		return []types.DiffWire{challengeSendWire(bytes.Repeat([]byte{0xab}, challengeSendBlob))}, nil
	})
	require.NoError(t, send(context.Background()))
	require.Equal(t, int32(1), reloads.Load())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2)
	require.Equal(t, bodies[0], bodies[1])
}

func TestChallengeCatchUpReloadFailureStopsTheRetry(t *testing.T) {
	var calls atomic.Int32
	rpc := newChallengeSendClient(t, func(transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
		calls.Add(1)
		return nil, storage.ErrStorageBusy
	})
	reloadErr := errors.New("journal read failed")
	send, _ := prepareTrackedPage(t, rpc, func() ([]types.DiffWire, error) {
		return nil, reloadErr
	})
	err := send(context.Background())
	require.ErrorIs(t, err, reloadErr)
	require.Equal(t, int32(1), calls.Load())
}

type failingSigner struct{ addr string }

func (s failingSigner) Sign([]byte) ([]byte, error) { return nil, errors.New("signer offline") }
func (s failingSigner) Address() string             { return s.addr }

func TestChallengeCatchUpSignerErrorSendsNothing(t *testing.T) {
	var calls atomic.Int32
	rpc := newChallengeSendClient(t, func(transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
		calls.Add(1)
		return &transport.ChallengeReceiptResponse{}, nil
	})
	clone, ok := rpc.CloneWithSigner(failingSigner{addr: "gonka1offline"}, 0).(host.ChallengeCatchUpPreparer)
	require.True(t, ok)
	send, err := clone.PrepareChallengeCatchUp(7, []types.DiffWire{challengeSendWire([]byte{0x12, 0x00})}, nil, "")
	require.NoError(t, err)
	err = send(context.Background())
	require.ErrorContains(t, err, "signer offline")
	var connectErr *connect.Error
	require.False(t, errors.As(err, &connectErr), "a local signing failure is not an executor answer")
	require.Zero(t, calls.Load())
}

func TestChallengeReceiptWiresCarriesThePayload(t *testing.T) {
	var got transport.ChallengeReceiptRequest
	rpc := newChallengeSendClient(t, func(req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
		got = req
		return &transport.ChallengeReceiptResponse{Receipt: []byte("receipt")}, nil
	})
	payload := &host.InferencePayload{Prompt: []byte("prompt"), Model: "llama", InputLength: 3, MaxTokens: 9, StartedAt: 1000}
	stored := bytes.Repeat([]byte{0xcd}, 300)
	receipt, mempool, err := rpc.ChallengeReceiptWires(context.Background(), 4, payload, []types.DiffWire{challengeSendWire(stored)}, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("receipt"), receipt)
	require.Empty(t, mempool)
	require.Equal(t, uint64(4), got.InferenceID)
	require.NotNil(t, got.Payload)
	require.Equal(t, payload.Prompt, got.Payload.Prompt)
	require.Equal(t, payload.Model, got.Payload.Model)
	require.Len(t, got.Diffs, 1)
	require.Equal(t, transport.DiffWireTxs(1, stored), got.Diffs[0].Txs)
}

func TestChallengeCatchUpOverGRPC(t *testing.T) {
	hostAddr := devtest.MustGenerateKey(t).Address()
	peer := devtest.MustGenerateKey(t)
	obs := newWireObserver()
	var got atomic.Pointer[transport.ChallengeReceiptRequest]
	lookup := challengeSendLookup{core: challengeSendCore{serve: func(req transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
		got.Store(&req)
		return &transport.ChallengeReceiptResponse{}, nil
	}}}
	h2, _ := startPeerRPCServerH2CObserved(t, hostAddr, rpcserver.PeerAuthConfig{Heartbeat: 50 * time.Millisecond}, lookup, obs)
	inf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "must not use HTTP/1.1 when h2 works", http.StatusTeapot)
	}))
	t.Cleanup(inf.Close)

	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	transport.ResetRPCH2ClientPoolForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)
	t.Cleanup(transport.ResetRPCH2ClientPoolForTest)
	pc := newTestPeerConn(t, inf, hostAddr, peer, transport.PeerConnConfig{
		DialSet:        transport.PeerRPCDialSet{H2URL: h2.URL},
		GRPC:           true,
		H2ProbeTimeout: 200 * time.Millisecond,
	})
	pc.Start()
	waitPeerReady(t, pc)
	require.True(t, pc.UsingGRPC())

	rpc := transport.NewRPCClient(transport.NewHTTPClient(inf.URL, "escrow-1", peer), pc, transport.ParseRPCEndpoints(transport.EndpointChallengeReceipt))
	t.Cleanup(rpc.Close)
	stored := bytes.Repeat([]byte{0xef}, 300)
	require.NoError(t, rpc.ChallengeCatchUp(context.Background(), 7, []types.DiffWire{challengeSendWire(stored)}, ""))
	req := got.Load()
	require.NotNil(t, req)
	require.Len(t, req.Diffs, 1)
	require.Equal(t, transport.DiffWireTxs(1, stored), req.Diffs[0].Txs)
	requireGRPCContentType(t, obs.requestContentType(rpcpbconnect.SessionServiceChallengeReceiptProcedure))
}

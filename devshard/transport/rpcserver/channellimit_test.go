package rpcserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	devtest "devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

type limitEnv struct {
	session rpcpbconnect.SessionServiceClient
	authc   rpcpbconnect.PeerAuthServiceClient
	payload rpcpbconnect.PayloadServiceClient
	gossip  rpcpbconnect.GossipServiceClient
	token   []byte
	signer  *signing.Secp256k1Signer
	now     func() time.Time
	url     string
	client  *http.Client
	auth    *PeerAuthHandler
}

func attachNowUnix(cfg PeerAuthConfig) int64 {
	if cfg.Now != nil {
		return cfg.Now().Unix()
	}
	return time.Now().Unix()
}

func startLimitEnv(t *testing.T, cfg PeerAuthConfig, lookup SessionLookup, opts ...MuxOption) limitEnv {
	t.Helper()
	auth := newTestAuth(cfg)
	mux := NewMux(auth, NewSessionHandler(lookup), opts...)
	srv := httptest.NewServer(withTestEscrow(mux))
	t.Cleanup(srv.Close)

	signer := devtest.MustGenerateKey(t)
	authc := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	nonce := []byte("channel-limit-attach-0123456")
	attached := attachAt(t, authc, signer, nonce, attachNowUnix(cfg))
	return limitEnv{
		session: rpcpbconnect.NewSessionServiceClient(srv.Client(), srv.URL),
		authc:   authc,
		payload: rpcpbconnect.NewPayloadServiceClient(srv.Client(), srv.URL),
		gossip:  rpcpbconnect.NewGossipServiceClient(srv.Client(), srv.URL),
		token:   attached.SessionToken,
		signer:  signer,
		now:     cfg.Now,
		url:     srv.URL,
		client:  srv.Client(),
		auth:    auth,
	}
}

func (e limitEnv) attachPeer(t *testing.T, signer *signing.Secp256k1Signer, nonce []byte) *rpcpb.AttachResponse {
	t.Helper()
	ts := time.Now().Unix()
	if e.now != nil {
		ts = e.now().Unix()
	}
	return attachAt(t, e.authc, signer, nonce, ts)
}

func (e limitEnv) getSigs() error {
	_, err := e.session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), e.token))
	return err
}

func (e limitEnv) getDiffs() error {
	_, err := e.session.GetDiffs(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetDiffsRequest{}), e.token))
	return err
}

func (e limitEnv) getMempool() error {
	_, err := e.session.GetMempool(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetMempoolRequest{}), e.token))
	return err
}

func (e limitEnv) getPayload() error {
	_, err := e.payload.GetPayload(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetPayloadRequest{}), e.token))
	return err
}

func requireResourceExhausted(t *testing.T, err error, msg string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), msg)
	requireRetryAfter(t, err)
}

func TestChannelLimit_AdvertisedMatchesConfig(t *testing.T) {
	limits := transport.ChannelLimitConfig{
		MessagesPerMin: 12,
		MessagesBurst:  5,
		MaxStreams:     4,
	}
	auth := newTestAuth(PeerAuthConfig{Limits: &limits})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	attached := attach(t, client, devtest.MustGenerateKey(t), []byte("advertise-limits-nonce-aaaaa"))
	require.Equal(t, uint32(12), attached.Limits.GetMessagesPerMin())
	require.Equal(t, uint32(5), attached.Limits.GetMessagesBurst())
	require.Equal(t, uint32(4), attached.Limits.GetMaxStreams())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpWeightPerMin())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpBurst())
}

func TestChannelLimit_AdvertisedMaxStreamsCappedByMaxConns(t *testing.T) {
	limits := transport.ChannelLimitConfig{
		MaxStreams: 256,
		MaxConns:   16,
	}
	auth := newTestAuth(PeerAuthConfig{Limits: &limits})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, NewSessionHandler(stubLookup{core: stubCore{}}))))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	attached := attach(t, client, devtest.MustGenerateKey(t), []byte("advertise-pool-cap-nonce-aaaa"))
	require.Equal(t, uint32(16), attached.Limits.GetMaxStreams())
}

func TestChannelLimit_DisabledAdvertisesUnlimited(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{Limits: &transport.ChannelLimitConfig{Disabled: true}})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL),
		devtest.MustGenerateKey(t), []byte("advertise-disabled-nonce-aaaa"))
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetMessagesPerMin())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetMessagesBurst())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetMaxStreams())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpWeightPerMin())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpBurst())
}

func TestChannelLimit_DisabledDisablesAttachFloor(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{
		AttachFloorPerMin: 1,
		Limits:            &transport.ChannelLimitConfig{Disabled: true},
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	for _, nonce := range [][]byte{
		[]byte("disabled-floor-nonce-0aaaa"),
		[]byte("disabled-floor-nonce-1bbbb"),
		[]byte("disabled-floor-nonce-2cccc"),
		[]byte("disabled-floor-nonce-3dddd"),
		[]byte("disabled-floor-nonce-4eeee"),
	} {
		_ = attach(t, client, devtest.MustGenerateKey(t), nonce)
	}
}

func TestChannelLimit_MessageBudgetAndRefill(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	e := startLimitEnv(t, PeerAuthConfig{
		Now: clock.Now,
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 60,
			MessagesBurst:  1,
		},
	}, stubLookup{core: stubCore{}})

	require.NoError(t, e.getSigs())
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")

	other := devtest.MustGenerateKey(t)
	otherTok := e.attachPeer(t, other, []byte("channel-limit-other-peer-aaaa"))
	_, err := e.session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), otherTok.SessionToken))
	require.NoError(t, err, "a second peer is unaffected")

	clock.Advance(time.Minute)
	require.NoError(t, e.getSigs())
}

func TestChannelLimit_PerMethodWeights(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	e := startLimitEnv(t, PeerAuthConfig{
		Now: clock.Now,
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 10,
			MessagesBurst:  10,
		},
	}, stubLookup{core: stubCore{}})

	for i := 0; i < 10; i++ {
		require.NoError(t, e.getSigs())
	}
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")

	clock.Advance(time.Minute)
	require.NoError(t, e.getSigs())
}

func TestChannelLimit_GetDiffsSharesPeerBudget(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: uint32(transport.RPCWeightGetDiffs),
			MessagesBurst:  uint32(transport.RPCWeightGetDiffs),
		},
	}, stubLookup{core: stubCore{member: true}}, WithPayloadService(NewPayloadHandler(stubLookup{core: stubCore{member: true}},
		func(context.Context, SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
			return &rpcpb.GetPayloadResponse{}, nil
		})))

	require.NoError(t, e.getDiffs())
	requireResourceExhausted(t, e.getDiffs(), "rate limit exceeded")
	requireResourceExhausted(t, e.getMempool(), "rate limit exceeded")
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")

	other := devtest.MustGenerateKey(t)
	otherTok := e.attachPeer(t, other, []byte("channel-limit-diffs-peer-bbbb"))
	_, err := e.session.GetDiffs(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetDiffsRequest{}), otherTok.SessionToken))
	require.NoError(t, err)
}

func TestChannelLimit_GetDiffsOverdraftWhenBurstBelowWeight(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	e := startLimitEnv(t, PeerAuthConfig{
		Now: clock.Now,
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 60,
			MessagesBurst:  10,
		},
	}, stubLookup{core: stubCore{member: true}})

	require.NoError(t, e.getDiffs(), "GetDiffs weight 60 must overdraft a 10-token burst")
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")
	requireResourceExhausted(t, e.getDiffs(), "rate limit exceeded")

	clock.Advance(52 * time.Second)
	require.NoError(t, e.getSigs(), "cheap RPCs resume once overdraft recovers past 1 token")
	requireResourceExhausted(t, e.getDiffs(), "rate limit exceeded")

	clock.Advance(time.Minute)
	require.NoError(t, e.getDiffs())
}

func TestChannelLimit_ConcurrentPeerCharges(t *testing.T) {
	l := newChannelLimiter(transport.ChannelLimitConfig{
		MessagesPerMin: 6000,
		MessagesBurst:  600,
	}, time.Now)
	ctx := context.Background()
	proc := rpcpbconnect.SessionServiceGetSignaturesProcedure
	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			peer := fmt.Sprintf("peer-%d", i)
			for j := 0; j < 20; j++ {
				if err := l.charge(ctx, peer, proc); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestChannelLimit_AttachFloorBeforeVerify(t *testing.T) {
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{
		AttachFloorPerMin: 1,
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	_ = attach(t, client, devtest.MustGenerateKey(t), []byte("attach-floor-mux-nonce-aaaaa"))
	calls := spy.n.Load()
	req, err := signedAttach(devtest.MustGenerateKey(t), []byte("attach-floor-mux-nonce-bbbbb"), time.Now().Unix())
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(req))
	requireResourceExhausted(t, err, "too many attach attempts")
	require.Equal(t, calls, spy.n.Load(), "floor must fire before ECDSA")
}

func TestChannelLimit_AttachFloorIgnoresXRealIP(t *testing.T) {
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{
		AttachFloorPerMin: 1,
	})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	_ = attach(t, client, devtest.MustGenerateKey(t), []byte("attach-xreal-nonce-aaaaaaaa"))
	calls := spy.n.Load()

	req, err := signedAttach(devtest.MustGenerateKey(t), []byte("attach-xreal-nonce-bbbbbbbb"), time.Now().Unix())
	require.NoError(t, err)
	creq := connect.NewRequest(req)
	creq.Header().Set("X-Real-IP", "198.51.100.7")
	_, err = client.Attach(context.Background(), creq)
	requireResourceExhausted(t, err, "too many attach attempts")
	require.Equal(t, calls, spy.n.Load(), "floor must be process-wide, not keyed on X-Real-IP")
}

func TestChannelLimit_AttachFloorOnHTTP1(t *testing.T) {
	var proto atomic.Int32
	spy := &countingVerifier{inner: signing.NewSecp256k1Verifier()}
	auth := NewPeerAuthHandler(spy, testHostAddress, PeerAuthConfig{
		AttachFloorPerMin: 1,
	})
	mux := withTestEscrow(NewMux(auth, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAttachPath(r.URL.Path) {
			proto.Store(int32(r.ProtoMajor))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	_ = attach(t, client, devtest.MustGenerateKey(t), []byte("attach-http1-nonce-aaaaaaaa"))
	require.Equal(t, int32(1), proto.Load(), "no-proxy / B1 Attach is HTTP/1.1; proxy zones are absent")
	calls := spy.n.Load()
	req, err := signedAttach(devtest.MustGenerateKey(t), []byte("attach-http1-nonce-bbbbbbbb"), time.Now().Unix())
	require.NoError(t, err)
	_, err = client.Attach(context.Background(), connect.NewRequest(req))
	requireResourceExhausted(t, err, "too many attach attempts")
	require.Equal(t, calls, spy.n.Load(), "HTTP/1.1 child floor still fires without proxy zones")
}

func TestChannelLimit_GetDiffsFloodStarvesChatOnSharedPie(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Heartbeat: time.Hour,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     8,
			MessagesPerMin: uint32(transport.RPCWeightGetDiffs),
			MessagesBurst:  uint32(transport.RPCWeightGetDiffs),
		},
	}, stubLookup{core: stubCore{member: true, owner: true}})

	require.NoError(t, e.getDiffs())
	chat, err := e.session.Chat(context.Background(), withSession(
		connect.NewRequest(&rpcpb.SignedEnvelope{EscrowId: "1"}), e.token))
	require.NoError(t, err)
	require.False(t, chat.Receive())
	requireResourceExhausted(t, chat.Err(), "rate limit exceeded")
	_ = chat.Close()
}

func TestChannelLimit_KeyIntegrity(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 1,
			MessagesBurst:  1,
		},
	}, stubLookup{core: stubCore{}})

	require.NoError(t, e.getSigs())
	req := withSession(connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), e.token)
	req.Header().Set("x-devshard-escrow", "forged-escrow")
	req.Header().Set("X-Real-IP", "192.0.2.1")
	_, err := e.session.GetSignatures(context.Background(), req)
	requireResourceExhausted(t, err, "rate limit exceeded")
}

func TestChannelLimit_ConcurrentStreams(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Heartbeat: time.Hour,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     1,
			MessagesPerMin: transport.UnlimitedRPCLimit,
		},
	}, stubLookup{core: stubCore{}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := e.authc.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), e.token))
	require.NoError(t, err)
	require.True(t, stream.Receive(), stream.Err())

	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	second, err := e.authc.Watch(ctx2, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), e.token))
	require.NoError(t, err)
	require.False(t, second.Receive())
	requireResourceExhausted(t, second.Err(), "too many concurrent streams")
	_ = second.Close()

	cancel()
	_ = stream.Close()
	for stream.Receive() {
	}

	// The client sees cancel before the server leaves Watch and releases
	// the stream slot. Retry until that release; a single attempt races.
	again := watchAfterSlotRelease(t, e)
	_ = again.Close()
}

func watchAfterSlotRelease(t *testing.T, e limitEnv) *connect.ServerStreamForClient[rpcpb.SessionEvent] {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := e.authc.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), e.token))
		if err != nil {
			cancel()
			last = err
		} else if stream.Receive() {
			t.Cleanup(func() {
				cancel()
				_ = stream.Close()
			})
			return stream
		} else {
			last = stream.Err()
			_ = stream.Close()
			cancel()
		}
		if last != nil {
			code := connect.CodeOf(last)
			if code != connect.CodeResourceExhausted && code != connect.CodeAlreadyExists {
				t.Fatalf("watch reconnect: %v", last)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch slot not released: %v", last)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestChannelLimit_ChatStreamCapDoesNotChargeWeight(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Heartbeat: time.Hour,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     1,
			MessagesPerMin: 100,
			MessagesBurst:  10,
		},
	}, stubLookup{core: stubCore{}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watch, err := e.authc.Watch(ctx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), e.token))
	require.NoError(t, err)
	require.True(t, watch.Receive(), watch.Err())

	chat, err := e.session.Chat(context.Background(), withSession(
		connect.NewRequest(&rpcpb.SignedEnvelope{EscrowId: "1"}), e.token))
	require.NoError(t, err)
	require.False(t, chat.Receive())
	requireResourceExhausted(t, chat.Err(), "too many concurrent streams")
	_ = chat.Close()
	require.NoError(t, e.getSigs(), "stream-cap Chat must not spend weight 10")
}

type blockingChatCore struct {
	stubCore
}

func (c blockingChatCore) ServeInference(ctx context.Context, call transport.InferenceCall) error {
	if call.Sink != nil {
		_, _ = call.Sink.Write([]byte("x"))
		call.Sink.Flush()
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestChannelLimit_ChatReservesWatchSlot(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Heartbeat: time.Hour,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     2,
			MessagesPerMin: transport.UnlimitedRPCLimit,
		},
	}, stubLookup{core: blockingChatCore{stubCore: stubCore{owner: true}}})

	env, err := transport.SignEnvelope(e.signer, testEscrowID, nil, time.Now().Unix())
	require.NoError(t, err)

	chatCtx, cancelChat := context.WithCancel(context.Background())
	t.Cleanup(cancelChat)
	chat, err := e.session.Chat(chatCtx, withSession(connect.NewRequest(env), e.token))
	require.NoError(t, err)
	require.True(t, chat.Receive(), chat.Err())

	watchCtx, cancelWatch := context.WithCancel(context.Background())
	t.Cleanup(cancelWatch)
	watch, err := e.authc.Watch(watchCtx, withSession(connect.NewRequest(&rpcpb.WatchRequest{}), e.token))
	require.NoError(t, err)
	require.True(t, watch.Receive(), watch.Err())

	second, err := e.session.Chat(context.Background(), withSession(connect.NewRequest(env), e.token))
	require.NoError(t, err)
	require.False(t, second.Receive())
	requireResourceExhausted(t, second.Err(), "too many concurrent streams")
	_ = second.Close()

	cancelWatch()
	_ = watch.Close()
	cancelChat()
	_ = chat.Close()
}

func TestChannelLimit_ChatReservesWatchSlotLimiter(t *testing.T) {
	l := newChannelLimiter(transport.ChannelLimitConfig{
		MaxStreams:     2,
		MessagesPerMin: 10,
		MessagesBurst:  10,
	}, time.Now)
	ctx := context.Background()
	chat := rpcpbconnect.SessionServiceChatProcedure
	watch := rpcpbconnect.PeerAuthServiceWatchProcedure

	require.NoError(t, l.acquireStream(ctx, "p", chat))
	requireResourceExhausted(t, l.acquireStream(ctx, "p", chat), "too many concurrent streams")
	require.NoError(t, l.acquireStream(ctx, "p", watch), "Watch still connects at Chat cap max-1")
	require.Equal(t, 2, l.streams["p"].total)
	require.Equal(t, 1, l.streams["p"].chat)

	l.releaseStream("p", watch)
	require.NoError(t, l.acquireStream(ctx, "p", watch), "Watch reconnect after Chat-full")

	l2 := newChannelLimiter(transport.ChannelLimitConfig{
		MaxStreams:     2,
		MessagesPerMin: 10,
		MessagesBurst:  10,
	}, time.Now)
	require.NoError(t, l2.acquireStream(ctx, "p", watch))
	require.NoError(t, l2.acquireStream(ctx, "p", chat), "Watch first still leaves a Chat slot")
	requireResourceExhausted(t, l2.acquireStream(ctx, "p", chat), "too many concurrent streams")
	require.Equal(t, 2, l2.streams["p"].total)
}

func TestChannelLimit_ProcessChatCapAcrossPeers(t *testing.T) {
	l := newChannelLimiter(transport.ChannelLimitConfig{
		MaxStreams:      256,
		MaxStreamsTotal: 32,
		MaxChatsTotal:   2,
		MessagesPerMin:  6000,
		MessagesBurst:   600,
	}, time.Now)
	ctx := context.Background()
	chat := rpcpbconnect.SessionServiceChatProcedure
	watch := rpcpbconnect.PeerAuthServiceWatchProcedure

	require.NoError(t, l.acquireStream(ctx, "a", chat))
	require.NoError(t, l.acquireStream(ctx, "b", chat))
	requireResourceExhausted(t, l.acquireStream(ctx, "c", chat), "too many concurrent chats")
	require.NoError(t, l.acquireStream(ctx, "w", watch), "Watch does not spend the Chat ceiling")
	require.Equal(t, 2, l.procChats)
	require.Equal(t, 3, l.procStreams)

	l.releaseStream("a", chat)
	require.NoError(t, l.acquireStream(ctx, "c", chat))
	require.Equal(t, 2, l.procChats)
}

func TestChannelLimit_ProcessStreamCapAcrossPeers(t *testing.T) {
	l := newChannelLimiter(transport.ChannelLimitConfig{
		MaxStreams:      256,
		MaxStreamsTotal: 2,
		MaxChatsTotal:   2,
		MessagesPerMin:  6000,
		MessagesBurst:   600,
	}, time.Now)
	ctx := context.Background()
	watch := rpcpbconnect.PeerAuthServiceWatchProcedure

	require.NoError(t, l.acquireStream(ctx, "a", watch))
	require.NoError(t, l.acquireStream(ctx, "b", watch))
	requireResourceExhausted(t, l.acquireStream(ctx, "c", watch), "too many concurrent streams")
	l.releaseStream("a", watch)
	require.NoError(t, l.acquireStream(ctx, "c", watch))
	require.Equal(t, 2, l.procStreams)
	require.Equal(t, 0, l.procChats)
}

func TestChannelLimit_ProcessStreamCapRecordsStreamsZone(t *testing.T) {
	now := time.Unix(1_710_000_000, 0)
	limits := transport.ChannelLimitConfig{
		MaxStreams:      256,
		MaxStreamsTotal: 32,
		MaxChatsTotal:   1,
		MessagesPerMin:  6000,
		MessagesBurst:   600,
	}
	auth := newTestAuth(PeerAuthConfig{
		Now:    func() time.Time { return now },
		Limits: &limits,
	})
	ctx := context.Background()
	chat := rpcpbconnect.SessionServiceChatProcedure
	require.NoError(t, auth.limiter.acquireStream(ctx, "a", chat))
	err := auth.limiter.acquireStream(ctx, "b", chat)
	requireResourceExhausted(t, err, "too many concurrent chats")
	// Same observe the streaming interceptor runs on acquireStream failure.
	auth.observeRPC(ctx, chat, "b", true, true, false, false)

	snap := auth.Traffic().Snapshot(now.Add(time.Minute))
	var banned uint64
	for _, z := range snap.Host.Zones {
		if z.Zone == transport.RPCZoneStreams {
			banned = z.Banned
		}
	}
	require.Equal(t, uint64(1), banned)
}

func TestChannelLimit_MaxConnsCapsStreamAcquire(t *testing.T) {
	l := newChannelLimiter(transport.ChannelLimitConfig{
		MaxStreams:     256,
		MaxConns:       2,
		MessagesPerMin: 10,
		MessagesBurst:  10,
	}, time.Now)
	ctx := context.Background()
	chat := rpcpbconnect.SessionServiceChatProcedure
	watch := rpcpbconnect.PeerAuthServiceWatchProcedure
	require.NoError(t, l.acquireStream(ctx, "p", chat))
	requireResourceExhausted(t, l.acquireStream(ctx, "p", chat), "too many concurrent streams")
	require.NoError(t, l.acquireStream(ctx, "p", watch), "Watch still connects at the pool min")
	require.Equal(t, 2, l.streams["p"].total)
	require.Equal(t, uint32(2), l.advertised().GetMaxStreams())
}

func TestChannelLimit_ChatRateLimitRecordsNoReceipt(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     8,
			MessagesPerMin: 10,
			MessagesBurst:  10,
		},
	}, stubLookup{core: stubCore{}})

	counter := observability.RequestTerminalCounterForTest(
		observability.TerminalNoReceiptInterrupted, observability.ReasonRateLimited)
	before := promtest.ToFloat64(counter)

	first, err := e.session.Chat(context.Background(), withSession(
		connect.NewRequest(&rpcpb.SignedEnvelope{EscrowId: "1"}), e.token))
	require.NoError(t, err)
	require.False(t, first.Receive())
	_ = first.Close()

	second, err := e.session.Chat(context.Background(), withSession(
		connect.NewRequest(&rpcpb.SignedEnvelope{EscrowId: "1"}), e.token))
	require.NoError(t, err)
	require.False(t, second.Receive())
	requireResourceExhausted(t, second.Err(), "rate limit exceeded")
	_ = second.Close()
	require.Equal(t, 1.0, promtest.ToFloat64(counter)-before, "throttled Chat must record FailNoReceipt")
}

func TestChannelLimit_ChatChargesAfterAcquire(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     8,
			MessagesPerMin: 10,
			MessagesBurst:  10,
		},
	}, stubLookup{core: stubCore{}})

	chat, err := e.session.Chat(context.Background(), withSession(
		connect.NewRequest(&rpcpb.SignedEnvelope{EscrowId: "1"}), e.token))
	require.NoError(t, err)
	require.False(t, chat.Receive())
	require.NotEqual(t, connect.CodeUnimplemented, connect.CodeOf(chat.Err()))
	_ = chat.Close()
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")
}

func TestChannelLimit_UnimplementedDoesNotCharge(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 1,
			MessagesBurst:  1,
		},
	}, stubLookup{core: stubCore{}})

	counter := observability.RequestTerminalCounterForTest(
		observability.TerminalNoReceiptInterrupted, observability.ReasonRateLimited)
	before := promtest.ToFloat64(counter)

	_, err := e.gossip.Txs(context.Background(), withSession(
		connect.NewRequest(&rpcpb.SignedEnvelope{}), e.token))
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err), "unmounted Gossip must not spend the peer burst")

	req, err := http.NewRequest(http.MethodPost, e.url+"/not-a-procedure", bytes.NewReader([]byte("x")))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/proto")
	SetSessionHeader(req.Header, e.token)
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)

	require.NoError(t, e.getSigs(), "unimplemented paths must leave the burst for a real RPC")
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")
	require.Equal(t, 0.0, promtest.ToFloat64(counter)-before, "unimplemented paths must not record FailNoReceipt")
}

func TestChannelLimit_SelfThrottlingClient(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	e := startLimitEnv(t, PeerAuthConfig{
		Now: clock.Now,
		Limits: &transport.ChannelLimitConfig{
			// Production default: burst is 10% of the minute, not the minute.
			MessagesPerMin: 20,
		},
	}, stubLookup{core: stubCore{}})

	limits := e.attachPeer(t, devtest.MustGenerateKey(t), []byte("self-throttle-limits-aaaaaa")).Limits
	require.Equal(t, uint32(20), limits.GetMessagesPerMin())
	require.Equal(t, uint32(2), limits.GetMessagesBurst())
	for i := uint32(0); i < limits.GetMessagesBurst(); i++ {
		require.NoError(t, e.getSigs())
	}
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")

	clock.Advance(time.Minute)
	for i := uint32(0); i < limits.GetMessagesBurst(); i++ {
		require.NoError(t, e.getSigs())
	}
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")
}

func TestChannelLimit_ExplicitMessagesBurstPulse(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	e := startLimitEnv(t, PeerAuthConfig{
		Now: clock.Now,
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 100,
			MessagesBurst:  3,
		},
	}, stubLookup{core: stubCore{}})

	limits := e.attachPeer(t, devtest.MustGenerateKey(t), []byte("explicit-burst-limits-aaaaa")).Limits
	require.Equal(t, uint32(100), limits.GetMessagesPerMin())
	require.Equal(t, uint32(3), limits.GetMessagesBurst())
	for i := uint32(0); i < limits.GetMessagesBurst(); i++ {
		require.NoError(t, e.getSigs())
	}
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")

	clock.Advance(time.Minute)
	for i := uint32(0); i < limits.GetMessagesBurst(); i++ {
		require.NoError(t, e.getSigs())
	}
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")
}

func TestChannelLimit_UnlimitedMessagesAdvertisesUnlimitedBurst(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{Limits: &transport.ChannelLimitConfig{
		MessagesPerMin: transport.UnlimitedRPCLimit,
	}})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	attached := attach(t, rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL),
		devtest.MustGenerateKey(t), []byte("advertise-unlimited-burst-aaa"))
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetMessagesPerMin())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetMessagesBurst())
}

func TestPeerAuth_AttachWatch_AdvertisedWeightCaps(t *testing.T) {
	auth := newTestAuth(PeerAuthConfig{})
	srv := httptest.NewServer(withTestEscrow(NewMux(auth, nil)))
	t.Cleanup(srv.Close)
	client := rpcpbconnect.NewPeerAuthServiceClient(srv.Client(), srv.URL)
	attached := attach(t, client, devtest.MustGenerateKey(t), []byte("advertise-default-get-caps-aa"))
	require.Equal(t, transport.DefaultRPCMessagesPerMin, attached.Limits.GetMessagesPerMin())
	require.Equal(t, transport.DefaultRPCMessagesBurst, attached.Limits.GetMessagesBurst())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpWeightPerMin())
	require.Equal(t, transport.UnlimitedRPCLimit, attached.Limits.GetIpBurst())
	require.Equal(t, transport.DefaultRPCMaxStreams, attached.Limits.GetMaxStreams())
}

func overflowTestLimiter(now func() time.Time, maxEntries int) *channelLimiter {
	return newChannelLimiter(transport.ChannelLimitConfig{
		MessagesPerMin: 10,
		MessagesBurst:  10,
		MaxStreams:     2,
		MaxEntries:     maxEntries,
	}, now)
}

func TestChannelLimit_OverflowPeerRefusesWithoutReset(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	l := overflowTestLimiter(clock.Now, 2)
	ctx := context.Background()
	proc := rpcpbconnect.SessionServiceGetSignaturesProcedure

	for i := 0; i < 10; i++ {
		require.NoError(t, l.charge(ctx, "a", proc))
	}
	requireResourceExhausted(t, l.charge(ctx, "a", proc), "rate limit exceeded")
	require.NoError(t, l.charge(ctx, "b", proc))
	requireResourceExhausted(t, l.charge(ctx, "c", proc), "too many peers")

	requireResourceExhausted(t, l.charge(ctx, "a", proc), "rate limit exceeded")
	require.NoError(t, l.charge(ctx, "b", proc), "named peer b keeps its remaining advertised burst")
	requireResourceExhausted(t, l.charge(ctx, "c", proc), "too many peers")
	requireResourceExhausted(t, l.charge(ctx, "d", proc), "too many peers")

	require.Contains(t, l.shared, "a")
	require.Contains(t, l.shared, "b")
	require.NotContains(t, l.shared, "c")
	require.NotContains(t, l.shared, "d")
	require.Equal(t, 2, len(l.shared))
}

func TestChannelLimit_OverflowEvictsIdleNamedKeys(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	l := overflowTestLimiter(clock.Now, 2)
	ctx := context.Background()
	proc := rpcpbconnect.SessionServiceGetSignaturesProcedure

	require.NoError(t, l.charge(ctx, "a", proc))
	require.NoError(t, l.charge(ctx, "b", proc))
	clock.Advance(time.Minute)
	require.NoError(t, l.charge(ctx, "c", proc))

	require.Contains(t, l.shared, "c")
	require.NotContains(t, l.shared, "a")
	require.NotContains(t, l.shared, "b")
	require.NoError(t, l.charge(ctx, "a", proc), "an idle peer can re-enter as a named key")
}

func TestChannelLimit_OverflowEvictsIdlePartial(t *testing.T) {
	clock := &testClock{t: time.Unix(1_700_000_000, 0)}
	max := channelLimiterEvictBatch * 2
	l := overflowTestLimiter(clock.Now, max)
	ctx := context.Background()
	proc := rpcpbconnect.SessionServiceGetSignaturesProcedure
	for i := 0; i < max; i++ {
		require.NoError(t, l.charge(ctx, fmt.Sprintf("p%d", i), proc))
	}
	requireResourceExhausted(t, l.charge(ctx, "new", proc), "too many peers")
	require.LessOrEqual(t, l.evictVisited, channelLimiterEvictBatch,
		"at-cap insert must not scan the whole map")
	require.Equal(t, max, len(l.shared))
	require.NotContains(t, l.shared, "new")

	clock.Advance(time.Minute)
	require.NoError(t, l.charge(ctx, "new", proc))
	require.Contains(t, l.shared, "new")
	require.Less(t, len(l.shared), max, "a batch of idle keys must make room")
	require.LessOrEqual(t, l.evictVisited, channelLimiterEvictBatch)
}

func TestAttachRing_DropExpiredFromHead(t *testing.T) {
	var r attachRing
	r.ensure(4)
	t0 := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		r.push(t0.Add(time.Duration(i) * time.Second))
	}
	require.Equal(t, 3, r.n)
	r.dropExpired(t0.Add(time.Second))
	ts, ok := r.oldest()
	require.True(t, ok)
	require.Equal(t, t0.Add(2*time.Second), ts)
	require.Equal(t, 1, r.n)
	r.removeLastEqual(t0.Add(2 * time.Second))
	require.Equal(t, 0, r.n)
}

func TestAttachRing_GrowsByDoublingNotLimit(t *testing.T) {
	var r attachRing
	r.ensure(transport.DefaultRPCAttachFloorPerMin)
	require.Equal(t, attachRingMinCap, len(r.buf), "first Attach must not reserve the whole floor")
	require.Equal(t, 0, r.n)

	t0 := time.Unix(1_700_000_000, 0)
	for i := 0; i < attachRingMinCap; i++ {
		r.push(t0.Add(time.Duration(i) * time.Second))
	}
	require.Equal(t, attachRingMinCap, r.n)
	require.Equal(t, attachRingMinCap, len(r.buf))

	r.ensure(transport.DefaultRPCAttachFloorPerMin)
	require.Equal(t, attachRingMinCap*2, len(r.buf))
	require.Equal(t, attachRingMinCap, r.n)
	ts, ok := r.oldest()
	require.True(t, ok)
	require.Equal(t, t0, ts)
}

func TestAttachRing_GrowsUpToLimit(t *testing.T) {
	const limit = 40
	var r attachRing
	t0 := time.Unix(1_700_000_000, 0)
	for i := 0; i < limit; i++ {
		r.ensure(limit)
		r.push(t0.Add(time.Duration(i) * time.Second))
	}
	require.Equal(t, limit, r.n)
	require.Equal(t, limit, len(r.buf))
	r.ensure(limit)
	r.push(t0.Add(time.Hour))
	require.Equal(t, limit, r.n, "push must not grow past the floor")
}

func TestAttachRing_EnsureDoesNotAllocateHugeLimit(t *testing.T) {
	var r attachRing
	r.ensure(50_000_000)
	require.Equal(t, attachRingMinCap, len(r.buf))
	require.LessOrEqual(t, len(r.buf), transport.MaxRPCAttachFloorPerMin)
}

func TestChannelLimit_OverflowRefusesNewStreamPeers(t *testing.T) {
	l := overflowTestLimiter(time.Now, 1)
	ctx := context.Background()
	proc := rpcpbconnect.PeerAuthServiceWatchProcedure

	require.NoError(t, l.acquireStream(ctx, "a", proc))
	requireResourceExhausted(t, l.acquireStream(ctx, "b", proc), "too many concurrent streams")
	require.NoError(t, l.acquireStream(ctx, "a", proc), "an existing stream peer is not reset")
	requireResourceExhausted(t, l.acquireStream(ctx, "a", proc), "too many concurrent streams")
	require.Equal(t, 1, len(l.streams))
	require.Equal(t, 2, l.streams["a"].total)

	l.releaseStream("a", proc)
	l.releaseStream("a", proc)
	require.NoError(t, l.acquireStream(ctx, "b", proc))
	require.Equal(t, 1, l.streams["b"].total)
	require.NotContains(t, l.streams, "a")
}

func TestChannelLimit_OverflowDoesNotResetHTTP(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Limits: &transport.ChannelLimitConfig{
			MessagesPerMin: 1,
			MessagesBurst:  1,
			MaxEntries:     2,
		},
	}, stubLookup{core: stubCore{}})

	require.NoError(t, e.getSigs())
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")

	p2 := e.attachPeer(t, devtest.MustGenerateKey(t), []byte("overflow-http-peer-bbbbbbbb"))
	_, err := e.session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), p2.SessionToken))
	require.NoError(t, err)

	p3 := e.attachPeer(t, devtest.MustGenerateKey(t), []byte("overflow-http-peer-cccccccc"))
	_, err = e.session.GetSignatures(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetSignaturesRequest{Nonce: 1}), p3.SessionToken))
	requireResourceExhausted(t, err, "too many peers")
	requireResourceExhausted(t, e.getSigs(), "rate limit exceeded")
}

func TestTokenBucket_Overdraft(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := newTokenBucket(60, 10, now)
	ok, _ := b.allow(now, 60)
	require.True(t, ok)
	ok, retry := b.allow(now, 1)
	require.False(t, ok)
	require.Equal(t, time.Duration(51.0/60.0*float64(time.Minute)), retry)

	later := now.Add(52 * time.Second)
	ok, _ = b.allow(later, 1)
	require.True(t, ok)
	ok, _ = b.allow(later, 60)
	require.False(t, ok)
}

package rpcserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
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

// ownerChatCore is a blocking Chat core that is the owner only for owner.
type ownerChatCore struct {
	blockingChatCore
	owner string
}

func (c ownerChatCore) IsOwner(addr string) bool { return addr == c.owner }

func chatCounts(l *channelLimiter) (pending, chats int) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	return l.procPendingChats, l.procChats
}

func waitChatCounts(t *testing.T, l *channelLimiter, pending, chats int) {
	t.Helper()
	require.Eventually(t, func() bool {
		p, c := chatCounts(l)
		return p == pending && c == chats
	}, 3*time.Second, 5*time.Millisecond, "want pending=%d chats=%d", pending, chats)
}

// stalledChat opens a Chat whose envelope never arrives. closeBody ends the
// request body.
func stalledChat(t *testing.T, e limitEnv, token []byte) (done <-chan struct{}, closeBody func()) {
	t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, e.url+rpcpbconnect.SessionServiceChatProcedure, pr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	SetSessionHeader(req.Header, token)
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		resp, err := e.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	closeBody = func() { _ = pw.Close() }
	t.Cleanup(closeBody)
	return ch, closeBody
}

func signedChat(t *testing.T, signer *signing.Secp256k1Signer) *rpcpb.SignedEnvelope {
	t.Helper()
	env, err := transport.SignEnvelope(signer, testEscrowID, nil, time.Now().Unix())
	require.NoError(t, err)
	return env
}

// eachChatProto runs fn over HTTP/1.1 and over h2c (the child's listen).
func eachChatProto(t *testing.T, fn func(t *testing.T, h2c bool)) {
	for _, h2c := range []bool{false, true} {
		name := "http1"
		if h2c {
			name = "h2c"
		}
		t.Run(name, func(t *testing.T) { fn(t, h2c) })
	}
}

func TestChat_NonOwnerStalledEnvelopeTakesNoSlot(t *testing.T) {
	eachChatProto(t, testChatNonOwnerStalledEnvelopeTakesNoSlot)
}

func testChatNonOwnerStalledEnvelopeTakesNoSlot(t *testing.T, h2c bool) {
	owner := devtest.MustGenerateKey(t)
	e := startLimitEnvProto(t, h2c, PeerAuthConfig{
		Heartbeat:       time.Hour,
		ChatReadTimeout: time.Hour,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:      256,
			MaxStreamsTotal: 2,
			MaxChatsTotal:   1,
			MessagesPerMin:  transport.UnlimitedRPCLimit,
		},
	}, stubLookup{core: ownerChatCore{owner: owner.Address()}})

	_, closeStalled := stalledChat(t, e, e.token)
	waitChatCounts(t, e.auth.limiter, 1, 0)

	ownerToken := e.attachPeer(t, owner, []byte("chat-owner-attach-nonce-0001")).SessionToken
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	chat, err := e.session.Chat(ctx, withSession(connect.NewRequest(signedChat(t, owner)), ownerToken))
	require.NoError(t, err)
	require.True(t, chat.Receive(), "the owner gets the only process Chat slot: %v", chat.Err())
	waitChatCounts(t, e.auth.limiter, 1, 1)

	closeStalled()
	cancel()
	_ = chat.Close()
	waitChatCounts(t, e.auth.limiter, 0, 0)
}

func TestChat_ReadDeadlineEndsUnreadEnvelope(t *testing.T) {
	eachChatProto(t, testChatReadDeadlineEndsUnreadEnvelope)
}

func testChatReadDeadlineEndsUnreadEnvelope(t *testing.T, h2c bool) {
	e := startLimitEnvProto(t, h2c, PeerAuthConfig{
		Heartbeat:       time.Hour,
		ChatReadTimeout: 50 * time.Millisecond,
	}, stubLookup{core: stubCore{owner: true}})
	counter := observability.PeerRPCChatDroppedForTest(observability.PeerRPCChatDroppedReadTimeout)
	before := promtest.ToFloat64(counter)

	done, _ := stalledChat(t, e, e.token)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("an unread Chat envelope must end at ChatReadTimeout")
	}
	waitChatCounts(t, e.auth.limiter, 0, 0)
	require.Equal(t, 1.0, promtest.ToFloat64(counter)-before)
}

func TestChat_PendingCapPerPeer(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Heartbeat:       time.Hour,
		ChatReadTimeout: time.Hour,
	}, stubLookup{core: inferenceStub{stubCore{owner: true}}})
	e.auth.limiter.maxPendingPerPeer = 1
	counter := observability.PeerRPCChatDroppedForTest(observability.PeerRPCChatDroppedPendingCap)
	before := promtest.ToFloat64(counter)

	_, closeStalled := stalledChat(t, e, e.token)
	waitChatCounts(t, e.auth.limiter, 1, 0)

	refused, err := e.session.Chat(context.Background(), withSession(connect.NewRequest(signedChat(t, e.signer)), e.token))
	require.NoError(t, err)
	require.False(t, refused.Receive())
	requireResourceExhausted(t, refused.Err(), "too many pending chats")
	_ = refused.Close()
	require.Equal(t, 1.0, promtest.ToFloat64(counter)-before)

	other := devtest.MustGenerateKey(t)
	otherToken := e.attachPeer(t, other, []byte("chat-pending-other-nonce-01")).SessionToken
	ok, err := e.session.Chat(context.Background(), withSession(connect.NewRequest(signedChat(t, other)), otherToken))
	require.NoError(t, err)
	for ok.Receive() {
	}
	require.NoError(t, ok.Err(), "another peer keeps its own pending slots")
	_ = ok.Close()

	closeStalled()
	waitChatCounts(t, e.auth.limiter, 0, 0)
}

func TestChat_OwnerAtPeerCapDoesNotBlockAnotherOwner(t *testing.T) {
	e := startLimitEnv(t, PeerAuthConfig{
		Heartbeat: time.Hour,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:      2,
			MaxStreamsTotal: 5,
			MaxChatsTotal:   4,
			MessagesPerMin:  transport.UnlimitedRPCLimit,
		},
	}, stubLookup{core: blockingChatCore{stubCore: stubCore{owner: true}}})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hold, err := e.session.Chat(ctx, withSession(connect.NewRequest(signedChat(t, e.signer)), e.token))
	require.NoError(t, err)
	require.True(t, hold.Receive(), hold.Err())
	t.Cleanup(func() { _ = hold.Close() })

	second, err := e.session.Chat(context.Background(), withSession(connect.NewRequest(signedChat(t, e.signer)), e.token))
	require.NoError(t, err)
	require.False(t, second.Receive())
	requireResourceExhausted(t, second.Err(), "too many concurrent streams")
	_ = second.Close()

	other := devtest.MustGenerateKey(t)
	otherToken := e.attachPeer(t, other, []byte("chat-cap-other-owner-nonce1")).SessionToken
	otherChat, err := e.session.Chat(ctx, withSession(connect.NewRequest(signedChat(t, other)), otherToken))
	require.NoError(t, err)
	require.True(t, otherChat.Receive(), "a second owner still gets a Chat: %v", otherChat.Err())
	_ = otherChat.Close()
}

// floodChatCore writes incompressible events until a send fails.
type floodChatCore struct {
	stubCore
}

func (floodChatCore) ServeInference(ctx context.Context, call transport.InferenceCall) error {
	raw := make([]byte, 48<<10)
	for i := 0; i < 4096; i++ {
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		event := "data: " + base64.StdEncoding.EncodeToString(raw) + "\n\n"
		if _, err := call.Sink.Write([]byte(event)); err != nil {
			return err
		}
		call.Sink.Flush()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func TestChat_SendDeadlineFreesSlotWhenGatewayStopsReading(t *testing.T) {
	eachChatProto(t, testChatSendDeadlineFreesSlot)
}

func testChatSendDeadlineFreesSlot(t *testing.T, h2c bool) {
	e := startLimitEnvProto(t, h2c, PeerAuthConfig{
		Heartbeat:       time.Hour,
		ChatSendTimeout: 100 * time.Millisecond,
		Limits: &transport.ChannelLimitConfig{
			MaxStreams:     8,
			MessagesPerMin: transport.UnlimitedRPCLimit,
		},
	}, stubLookup{core: floodChatCore{stubCore{owner: true}}})
	counter := observability.PeerRPCChatDroppedForTest(observability.PeerRPCChatDroppedSendTimeout)
	before := promtest.ToFloat64(counter)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := e.session.Chat(ctx, withSession(connect.NewRequest(signedChat(t, e.signer)), e.token))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	require.Eventually(t, func() bool {
		return promtest.ToFloat64(counter)-before >= 1
	}, 10*time.Second, 10*time.Millisecond, "a Chat nobody reads must hit ChatSendTimeout")
	waitChatCounts(t, e.auth.limiter, 0, 0)
}

func TestChat_ReadDeadlineDoesNotEndLiveStream(t *testing.T) {
	eachChatProto(t, testChatReadDeadlineDoesNotEndLiveStream)
}

func testChatReadDeadlineDoesNotEndLiveStream(t *testing.T, h2c bool) {
	e := startLimitEnvProto(t, h2c, PeerAuthConfig{
		Heartbeat:       time.Hour,
		ChatReadTimeout: 50 * time.Millisecond,
	}, stubLookup{core: slowChatCore{stubCore: stubCore{owner: true}, gap: 300 * time.Millisecond}})

	stream, err := e.session.Chat(context.Background(), withSession(connect.NewRequest(signedChat(t, e.signer)), e.token))
	require.NoError(t, err)
	frames := 0
	for stream.Receive() {
		frames++
	}
	require.NoError(t, stream.Err(), "the read deadline must not cancel a Chat after its envelope is read")
	require.Positive(t, frames)
	_ = stream.Close()
}

// slowChatCore sends one event, waits past ChatReadTimeout, then a second.
type slowChatCore struct {
	stubCore
	gap time.Duration
}

func (c slowChatCore) ServeInference(ctx context.Context, call transport.InferenceCall) error {
	for i := 0; i < 2; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.gap):
			}
		}
		event := "data: " + base64.StdEncoding.EncodeToString(make([]byte, 1024)) + "\n\n"
		if _, err := call.Sink.Write([]byte(event)); err != nil {
			return err
		}
		call.Sink.Flush()
	}
	return nil
}

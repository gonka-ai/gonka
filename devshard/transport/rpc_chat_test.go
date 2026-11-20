package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"

	"common/chainoracle/blocks"
	"devshard"
	"devshard/chainoracle/blocks/failover"
	"devshard/chainoracle/blocks/headertime"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

func chatTestInferenceJSON(t *testing.T, env *serverTestEnv) []byte {
	t.Helper()
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	diffJSON, err := DiffToJSON(diff)
	require.NoError(t, err)
	body, err := json.Marshal(InferenceRequest{
		Diffs: []DiffJSON{diffJSON},
		Nonce: 1,
		Payload: &PayloadJSON{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		},
		Stream: true,
	})
	require.NoError(t, err)
	return body
}

func collectChatFrames(t *testing.T, env *serverTestEnv, body []byte) (chunks [][]byte, decoded string) {
	t.Helper()
	sink := NewChatFrameSink(func(chunk []byte) error {
		chunks = append(chunks, append([]byte(nil), chunk...))
		return nil
	})
	err := env.server.ServeInference(context.Background(), InferenceCall{
		SessionID: "escrow-1",
		Sender:    env.userSigner.Address(),
		Body:      body,
		Source:    "test",
		Sink:      sink,
	})
	require.NoError(t, err)
	require.NoError(t, sink.Close())
	decoded = gzipConcat(t, chunks)
	return chunks, decoded
}

type chatTipOracle struct {
	*mockoracle.Mock
	latestCalls atomic.Int64
	stall       atomic.Bool
}

func (o *chatTipOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	o.latestCalls.Add(1)
	if o.stall.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return o.Mock.Latest(ctx)
}

type delayedChatAcceptance struct{ clock *atomic.Int64 }

func (d delayedChatAcceptance) Check(types.EscrowState, []*types.DevshardTx) error {
	time.Sleep(time.Second)
	d.clock.Store(int64(time.Second))
	return nil
}

func TestServeInference_ProofTipAlsoSuppliesOutboundHeightSync(t *testing.T) {
	t.Run("fresh same-request tip", func(t *testing.T) { serveInferenceWithRequestTip(t, "", 2, 0) })
	t.Run("expired tip with stalled dAPI", func(t *testing.T) { serveInferenceWithRequestTip(t, "anchor", 3, 1) })
	t.Run("tip refreshed during signing", func(t *testing.T) {
		serveInferenceWithRequestTip(t, "signing", 3, 0)
	})
	t.Run("stale checkpoint without a tip sample", func(t *testing.T) {
		serveInferenceWithRequestTip(t, "stale checkpoint", 2, 1)
	})
	t.Run("refreshed anchor would contradict a heartbeat acknowledgment", func(t *testing.T) {
		serveInferenceWithRequestTip(t, "heartbeat", 3, 1)
	})
}

func serveInferenceWithRequestTip(t *testing.T, expireBefore string, wantDAPIReads, wantDirectReads int64) {
	t.Helper()
	const escrowID = "escrow-1"
	const balance = 1_000_000
	validator, owner := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	address, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	chain, err := mockoracle.NewMock(mockoracle.MockConfig{
		LightBlocks: true, ChainID: "chat-proof", InitialHeight: 100,
		Validators: []mockoracle.MockValidator{{Signer: validator, Address: address, Power: 10}},
		Start:      time.Now().Add(-10 * time.Second), BlockInterval: time.Second,
	})
	require.NoError(t, err)
	anchor, err := chain.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(1)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, bytes.Clone(anchor.BlockHash)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{validator})
	verifier := signing.NewSecp256k1Verifier()
	newState := func(source state.MainnetHeightSource) *state.StateMachine {
		store := testutil.MustMemoryStore(t, escrowID, owner.Address(), cfg, group, balance)
		sm, err := state.NewStateMachine(escrowID, cfg, group, balance, owner.Address(), verifier, store,
			state.WithVersion("v7"), state.WithMainnetHeights(source))
		require.NoError(t, err)
		return sm
	}
	producer := newState(chain)
	proof, err := producer.BuildMainnetHeightTx(t.Context())
	require.NoError(t, err)
	txs := []*types.DevshardTx{proof}
	var payload *PayloadJSON
	staleCheckpoint := expireBefore == "stale checkpoint"
	heartbeat := expireBefore == "heartbeat"
	if heartbeat {
		txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			ObservedHeight: uint64(anchor.Height), ObservedBlockHash: anchor.BlockHash,
			SlotsNum: 1, Reason: string(heightsync.ReasonQuietSession),
		}}})
	} else if !staleCheckpoint {
		txs = append(txs, testutil.StartTx(1))
		payload = &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
	}
	root, err := producer.ApplyLocal(1, txs)
	require.NoError(t, err)
	signedDiff := testutil.SignDiffWithRoot(t, owner, escrowID, 1, txs, root)
	diff, err := DiffToJSON(signedDiff)
	require.NoError(t, err)
	dapi := &chatTipOracle{Mock: chain}
	direct := &chatTipOracle{Mock: chain}
	oracle := failover.New(nil, dapi, direct, failover.WithDirectLightBlocks(chain))
	initial := time.Now()
	var clock atomic.Int64
	oracle.SetNow(func() time.Time { return initial.Add(time.Duration(clock.Load())) })
	var checker host.AcceptanceChecker
	if expireBefore == "signing" {
		checker = delayedChatAcceptance{clock: &clock}
	}
	times := headertime.New(headertime.WithLatestOracle(oracle), headertime.WithLightBlockSource(oracle))
	h, err := host.NewHost(newState(times), validator, stub.NewInferenceEngine(), escrowID, group, checker,
		host.WithChainOracle(oracle))
	require.NoError(t, err)
	diffs := []DiffJSON{diff}
	if staleCheckpoint {
		_, err := h.ApplyRecoveredDiffs(t.Context(), []types.Diff{signedDiff})
		require.NoError(t, err)
		diffs = nil
		for range state.MainnetCheckpointMaxLag + 1 {
			_, err := chain.AdvanceOne()
			require.NoError(t, err)
		}
		dapi.latestCalls.Store(0)
		direct.latestCalls.Store(0)
	}
	scheduler, err := heightsync.NewAnchorSchedulerFromOracle(10, 1, oracle)
	require.NoError(t, err)
	srv, err := NewServer(h, nil, verifier, owner.Address(), WithHeightSync(scheduler, oracle))
	require.NoError(t, err)
	body, err := json.Marshal(InferenceRequest{
		Diffs: diffs, Nonce: 1, Stream: true, Payload: payload,
	})
	require.NoError(t, err)
	var dapiReadsAtReceipt, directReadsAtReceipt int64
	var chunks [][]byte
	sink := NewChatFrameSink(func(chunk []byte) error {
		if len(chunks) == 0 {
			dapiReadsAtReceipt = dapi.latestCalls.Load()
			directReadsAtReceipt = direct.latestCalls.Load()
			dapi.stall.Store(false)
		}
		chunks = append(chunks, bytes.Clone(chunk))
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	require.NoError(t, srv.ServeInference(ctx, InferenceCall{
		SessionID: escrowID, Sender: owner.Address(), Body: body, Source: "test", Sink: sink,
		OnStreamStart: func() {
			if expireBefore == "anchor" || staleCheckpoint || heartbeat {
				clock.Store(int64(time.Second))
				dapi.stall.Store(true)
			}
			if heartbeat {
				_, err := chain.AdvanceOne()
				require.NoError(t, err)
			}
		},
	}))
	require.NoError(t, sink.Close())
	decoded := gzipConcat(t, chunks)
	require.Contains(t, decoded, `"devshard_receipt"`)
	if heartbeat {
		require.NotContains(t, decoded, `"height_sync"`, "the newer anchor would contradict the emitted acknowledgment")
		var ack *types.MsgHeightAck
		for _, tx := range h.MempoolTxs() {
			if tx.GetHeightAck() != nil {
				ack = tx.GetHeightAck()
				break
			}
		}
		require.NotNil(t, ack)
		require.EqualValues(t, anchor.Height, ack.ObservedHeight)
		require.Equal(t, anchor.BlockHash, ack.ObservedBlockHash)
		require.False(t, srv.HeightSyncMarks().HasKind(heightsync.MarkDisputeOriginator))
	} else {
		require.Contains(t, decoded, `"height_sync"`)
	}
	if staleCheckpoint {
		require.NotContains(t, decoded, `"state_sig"`, "the stale checkpoint contributes no state signature")
	} else {
		require.Regexp(t, `"state_sig":"[^"]+"`, decoded)
	}
	require.Equal(t, wantDAPIReads, dapiReadsAtReceipt, "dAPI RPCs before the signed receipt and anchor")
	require.Equal(t, wantDirectReads, directReadsAtReceipt, "chain RPCs before the signed receipt and anchor")
}

func TestServeInference_ChatGzipParityWithSSE(t *testing.T) {
	env := setupServerEnv(t)
	body := chatTestInferenceJSON(t, env)

	router := echo.New()
	router.HideBanner = true
	router.POST("/devshard/v2/sessions/:id/chat/completions", func(c echo.Context) error {
		return env.server.AuthMiddleware(env.server.RateLimitMiddleware(true)(env.server.HandleInference))(c)
	}, ResponseCompressionMiddleware)

	timestamp := time.Now().Unix()
	signature, err := SignRequest(env.userSigner, "escrow-1", body, timestamp)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost,
		"/devshard/v2/sessions/escrow-1/chat/completions", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderSignature, hex.EncodeToString(signature))
	request.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	request.Header.Set("Accept-Encoding", gzipEncoding)

	recorder := testutil.NewFlushRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code())
	sseCompressed := recorder.Body()
	require.NotEmpty(t, sseCompressed)

	env2 := setupServerEnv(t)
	body2 := chatTestInferenceJSON(t, env2)
	chunks, decoded := collectChatFrames(t, env2, body2)
	chatCompressed := bytes.Join(chunks, nil)
	require.Contains(t, decoded, "devshard_receipt")
	require.Contains(t, decoded, stubAnswerMarker)
	require.Contains(t, decoded, "[DONE]")
	require.Contains(t, decoded, "devshard_meta")

	// The Chat frames are one gzip window, so they stay close to the SSE
	// response gzipped whole. A slide into per-frame members is a
	// material regression (PR #1739). Allow a small slack for flush points.
	slack := len(sseCompressed)/10 + 64
	require.LessOrEqual(t, len(chatCompressed), len(sseCompressed)+slack,
		"Chat gzip %d vs SSE gzip %d", len(chatCompressed), len(sseCompressed))
}

func TestServeInference_ChatEventOrder(t *testing.T) {
	env := setupServerEnv(t)
	_, decoded := collectChatFrames(t, env, chatTestInferenceJSON(t, env))
	receiptAt := strings.Index(decoded, "devshard_receipt")
	stubAt := strings.Index(decoded, stubAnswerMarker)
	doneAt := strings.Index(decoded, "[DONE]")
	metaAt := strings.Index(decoded, "devshard_meta")
	require.GreaterOrEqual(t, receiptAt, 0)
	require.Greater(t, stubAt, receiptAt)
	require.Greater(t, doneAt, stubAt)
	require.Greater(t, metaAt, doneAt)
}

func TestServeInference_HeightSyncEnvelope(t *testing.T) {
	env := setupServerEnv(t)
	ir := InferenceRequest{}
	require.NoError(t, json.Unmarshal(chatTestInferenceJSON(t, env), &ir))
	hs := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       42,
		MainnetBlockHashHex: "abcd",
		TimestampUnixMs:     1,
		Direction:           "request",
	}
	body, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)
	_, decoded := collectChatFrames(t, env, body)
	require.Contains(t, decoded, "devshard_receipt")
	require.Contains(t, decoded, stubAnswerMarker)
}

func TestParseSSE_ChatGzipOversizeEvent(t *testing.T) {
	payload := "data: " + strings.Repeat("x", DefaultMaxSSEEventBytes) + "\n\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	client := streamBoundClient(DefaultMaxSSEStreamBytes)
	reader, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	defer reader.Close()
	_, err = client.parseSSEResponse(context.Background(), reader, nil, nil)
	require.ErrorIs(t, err, ErrSSEEventTooLarge)
}

func TestParseSSE_ChatGzipOversizeStream(t *testing.T) {
	payload := sseFrames(8, 64) + sseTerminator
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	client := streamBoundClient(int64(len(payload)) - 1)
	reader, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	require.NoError(t, err)
	defer reader.Close()
	_, err = client.parseSSEResponse(context.Background(), reader, nil, nil)
	require.ErrorIs(t, err, ErrSSEStreamTooLarge)
}

func TestServeInference_CancelDuringReceiptDelay(t *testing.T) {
	env := setupServerEnv(t, WithReceiptDelay(time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	var n int
	sink := NewChatFrameSink(func([]byte) error {
		n++
		return nil
	})
	err := env.server.ServeInference(ctx, InferenceCall{
		SessionID: "escrow-1",
		Sender:    env.userSigner.Address(),
		Body:      chatTestInferenceJSON(t, env),
		Sink:      sink,
	})
	require.NoError(t, err)
	require.NoError(t, sink.Close())
	require.Zero(t, n, "cancel before receipt must not emit frames")
	require.Less(t, time.Since(start), 500*time.Millisecond)
}

type countingInferenceEngine struct {
	inner *stub.InferenceEngine
	n     atomic.Int32
}

func (e *countingInferenceEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	e.n.Add(1)
	return e.inner.Execute(ctx, req)
}

func mempoolHasFinish(txs []*types.DevshardTx) bool {
	for _, tx := range txs {
		if tx.GetFinishInference() != nil {
			return true
		}
	}
	return false
}

// The receipt's ConfirmStart is already in the mempool when the write fails,
// so the host is bound to the inference and executes it without the client.
func TestServeInference_ReceiptFlushFailureStillExecutesDetached(t *testing.T) {
	engine := &countingInferenceEngine{inner: stub.NewInferenceEngine()}
	env := setupServerEnvEngine(t, engine, nil)
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	err := env.server.ServeInference(context.Background(), InferenceCall{
		SessionID: "escrow-1",
		Sender:    env.userSigner.Address(),
		Body:      chatTestInferenceJSON(t, env),
		Source:    "test",
		Sink:      sink,
	})
	require.ErrorContains(t, err, "peer gone")
	require.Eventually(t, func() bool {
		return engine.n.Load() == 1 && mempoolHasFinish(env.server.Host().MempoolTxs())
	}, 5*time.Second, 5*time.Millisecond, "a bound execution still queues its Finish")
}

func TestServeInference_TokenFlushFailureAfterReceiptStillFinishes(t *testing.T) {
	engine := &countingInferenceEngine{inner: stub.NewInferenceEngine()}
	env := setupServerEnvEngine(t, engine, nil)
	var n atomic.Int32
	sink := NewChatFrameSink(func([]byte) error {
		if n.Add(1) == 1 {
			return nil
		}
		return errors.New("peer gone")
	})
	err := env.server.ServeInference(context.Background(), InferenceCall{
		SessionID: "escrow-1",
		Sender:    env.userSigner.Address(),
		Body:      chatTestInferenceJSON(t, env),
		Source:    "test",
		Sink:      sink,
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), engine.n.Load(), "token-stream death after a sent receipt still executes")
	require.True(t, mempoolHasFinish(env.server.Host().MempoolTxs()))
}

func TestDefaultClientConfig_InferenceTimeout(t *testing.T) {
	cfg := DefaultClientConfig()
	require.Equal(t, 30*time.Minute, cfg.InferenceTimeout)
	require.Equal(t, 30*time.Second, cfg.QueryTimeout)
}

func TestRPCClient_SendStreamCapDoesNotSpendBudget(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1chatcap",
		Signer:      signer,
		DirectMux:   true,
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("refunded Chat must not wait on the peer budget")
			return nil
		},
	})
	t.Cleanup(pc.Close)
	pc.streams.apply(&rpcpb.RateLimits{MaxStreams: 1})
	pc.budget.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, time.Now())
	pc.setState(stateReady)
	pc.publishToken([]byte("tok-chat-cap"), time.Now().Add(time.Hour))
	require.True(t, pc.acquireStream(), "Watch holds the only advertised slot")

	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "escrow-1", signer), pc, ParseRPCEndpoints(EndpointChat))
	_, err := rpc.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, nil, nil)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "too many concurrent streams")
	require.NoError(t, pc.takePeerBudget(context.Background(), rpcpbconnect.SessionServiceGetSignaturesProcedure))
}

func TestRPCClient_SendChatCapLeavesWatchSlot(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1chatwatch",
		Signer:      signer,
		DirectMux:   true,
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("stream-cap Chat must not wait on the peer budget")
			return nil
		},
	})
	t.Cleanup(pc.Close)
	pc.streams.apply(&rpcpb.RateLimits{MaxStreams: 2})
	pc.budget.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, time.Now())
	pc.setState(stateReady)
	pc.publishToken([]byte("tok-chat-watch"), time.Now().Add(time.Hour))
	require.True(t, pc.acquireStream(), "Watch holds one slot")
	require.True(t, pc.acquireChatStream(), "one Chat fits under max-1")

	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "escrow-1", signer), pc, ParseRPCEndpoints(EndpointChat))
	_, err := rpc.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, nil, nil)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "too many concurrent streams")
	require.NoError(t, pc.takePeerBudget(context.Background(), rpcpbconnect.SessionServiceGetSignaturesProcedure))
}

func TestRPCClient_SendWaitReadyDoesNotTakeBudget(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1chatwait",
		Signer:      signer,
		DirectMux:   true,
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("WaitReady miss must not wait on the peer budget")
			return nil
		},
	})
	t.Cleanup(pc.Close)
	pc.budget.apply(&rpcpb.RateLimits{MessagesPerMin: 6000, MessagesBurst: 10}, time.Now())

	cfg := DefaultClientConfig()
	cfg.InferenceTimeout = 80 * time.Millisecond
	rpc := NewRPCClient(NewHTTPClient("http://127.0.0.1:1", "escrow-1", signer, cfg), pc, ParseRPCEndpoints(EndpointChat))
	start := time.Now()
	_, err := rpc.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:    []byte("x"),
			Model:     "llama",
			MaxTokens: 1,
			StartedAt: 1,
		},
	}, nil, nil)
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.Less(t, time.Since(start), time.Second)
	require.NoError(t, pc.takePeerBudget(context.Background(), rpcpbconnect.SessionServiceChatProcedure),
		"WaitReady miss must not spend Chat weight 10")
}

func TestParseChatStream_ZeroFramesTruncated(t *testing.T) {
	c := &RPCClient{HTTPClient: &HTTPClient{config: DefaultClientConfig()}}
	_, err := c.parseChatStream(context.Background(), nil, io.Discard, nil)
	require.ErrorIs(t, err, ErrSSEStreamTruncated)
}

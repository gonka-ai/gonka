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
	"common/chainoracle/blocks/lightproof"
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
	offline     atomic.Bool
}

func (o *chatTipOracle) Latest(ctx context.Context) (*blocks.Header, error) {
	o.latestCalls.Add(1)
	if o.stall.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if o.offline.Load() {
		return nil, blocks.ErrHeaderNotFound
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
	t.Run("acknowledgment arrives during streaming", func(t *testing.T) {
		serveInferenceWithRequestTip(t, "streaming", 2, 0)
	})
	t.Run("first live tip becomes available before the response", func(t *testing.T) {
		serveInferenceWithRequestTip(t, "cold retry", 1, 2)
	})
}

func serveInferenceWithRequestTip(t *testing.T, expireBefore string, wantDAPIReads, wantDirectReads int64) {
	t.Helper()
	const escrowID = "escrow-1"
	const balance = 1_000_000
	validator, owner := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	address, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	blockAge := 10 * time.Second
	coldRetry := expireBefore == "cold retry"
	if coldRetry {
		blockAge = 3 * time.Minute
	}
	chain, err := mockoracle.NewMock(mockoracle.MockConfig{
		LightBlocks: true, ChainID: "chat-proof", InitialHeight: 100,
		Validators: []mockoracle.MockValidator{{Signer: validator, Address: address, Power: 10}},
		Start:      time.Now().Add(-blockAge), BlockInterval: time.Second,
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
	var proof *types.DevshardTx
	if coldRetry {
		pins := lightproof.Checkpoint{Height: anchor.Height, Hash: anchor.BlockHash}
		carried, checkpoint, err := lightproof.Build(t.Context(), pins, anchor.Height, chain)
		require.NoError(t, err)
		proof = &types.DevshardTx{Tx: &types.DevshardTx_MainnetHeight{MainnetHeight: &types.MsgMainnetHeight{
			Height: checkpoint.Height, TimeUnixNano: checkpoint.Time.UnixNano(), LightBlocks: carried,
		}}}
	} else {
		proof, err = producer.BuildMainnetHeightTx(t.Context())
		require.NoError(t, err)
	}
	txs := []*types.DevshardTx{proof}
	var payload *PayloadJSON
	staleCheckpoint := expireBefore == "stale checkpoint" || coldRetry
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
		if coldRetry {
			dapi.offline.Store(true)
			direct.offline.Store(true)
		} else {
			for range state.MainnetCheckpointMaxLag + 1 {
				_, err := chain.AdvanceOne()
				require.NoError(t, err)
			}
		}
		dapi.latestCalls.Store(0)
		direct.latestCalls.Store(0)
	}
	if expireBefore == "streaming" {
		ack := &types.MsgHeightAck{SlotId: 0, RefNonce: 1, ObservedHeight: uint64(anchor.Height - 1), ObservedBlockHash: []byte{1}}
		require.NoError(t, heightsync.SignAck(validator, ack))
		h.HostMempool().AddTx(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}})
	}
	k := uint64(10)
	if expireBefore == "streaming" {
		k = 1 // Every nonce would ordinarily have an anchor.
	}
	scheduler, err := heightsync.NewAnchorSchedulerFromOracle(k, 1, oracle)
	require.NoError(t, err)
	srv, err := NewServer(h, nil, verifier, owner.Address(), WithHeightSync(scheduler, oracle))
	require.NoError(t, err)
	body, err := json.Marshal(InferenceRequest{
		Diffs: diffs, Nonce: 1, Stream: true, Payload: payload,
	})
	require.NoError(t, err)
	var dapiReadsAtReceipt, directReadsAtReceipt int64
	var chunks [][]byte
	var duringStream *types.DevshardTx
	sink := NewChatFrameSink(func(chunk []byte) error {
		if len(chunks) == 0 {
			dapiReadsAtReceipt = dapi.latestCalls.Load()
			directReadsAtReceipt = direct.latestCalls.Load()
			dapi.stall.Store(false)
			if expireBefore == "streaming" {
				higher, err := chain.AdvanceOne()
				require.NoError(t, err)
				heartbeatProof, err := producer.BuildMainnetHeightTx(t.Context())
				require.NoError(t, err)
				heartbeatTxs := []*types.DevshardTx{heartbeatProof, {Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
					ObservedHeight: uint64(higher.Height), ObservedBlockHash: higher.BlockHash,
					SlotsNum: 1, Reason: string(heightsync.ReasonQuietSession),
				}}}}
				root, err := producer.ApplyLocal(2, heartbeatTxs)
				require.NoError(t, err)
				heartbeatDiff := testutil.SignDiffWithRoot(t, owner, escrowID, 2, heartbeatTxs, root)
				response, err := h.HandleRequest(t.Context(), host.HostRequest{Nonce: 2, Diffs: []types.Diff{heartbeatDiff}})
				require.NoError(t, err)
				for _, tx := range response.Mempool {
					if ack := tx.GetHeightAck(); ack != nil && ack.RefNonce == 2 {
						duringStream = tx
						break
					}
				}
				require.NotNil(t, duringStream)
			}
		}
		chunks = append(chunks, bytes.Clone(chunk))
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	require.NoError(t, srv.ServeInference(ctx, InferenceCall{
		SessionID: escrowID, Sender: owner.Address(), Body: body, Source: "test", Sink: sink,
		OnStreamStart: func() {
			if coldRetry {
				direct.offline.Store(false)
			} else if expireBefore == "anchor" || staleCheckpoint || heartbeat {
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
	if expireBefore == "streaming" {
		client := &HTTPClient{}
		response, err := client.parseSSEResponse(t.Context(), strings.NewReader(decoded), io.Discard, nil)
		require.NoError(t, err)
		require.NotNil(t, duringStream)
		for _, tx := range response.Mempool {
			require.Nil(t, tx.GetHeightAck(), "the late acknowledgment was never paired with this anchor")
		}
		require.Contains(t, h.MempoolTxs(), duringStream, "the acknowledgment remains available for the next exchange")
		_, err = chain.AdvanceOne()
		require.NoError(t, err)
		retryBody, err := json.Marshal(InferenceRequest{Nonce: 2, Stream: true})
		require.NoError(t, err)
		_, retry := collectChatFrames(t, &serverTestEnv{server: srv, userSigner: owner}, retryBody)
		next, err := client.parseSSEResponse(t.Context(), strings.NewReader(retry), io.Discard, nil)
		require.NoError(t, err)
		require.NotContains(t, retry, `"height_sync"`, "the advanced tip cannot bind the earlier ACK")
		var delivered *types.MsgHeightAck
		for _, tx := range next.Mempool {
			if ack := tx.GetHeightAck(); ack != nil && ack.RefNonce == 2 {
				delivered = ack
				break
			}
		}
		require.NotNil(t, delivered, "the acknowledgment is sent on the next compatible response")
		require.Equal(t, duringStream.GetHeightAck().ObservedHeight, delivered.ObservedHeight)

		// A different pending signature for the same slot and heartbeat no
		// longer suppresses anchors once the accepted diff covers the slot.
		duplicate := &types.MsgHeightAck{
			SlotId: 0, RefNonce: 2, ObservedHeight: uint64(anchor.Height), ObservedBlockHash: anchor.BlockHash,
		}
		require.NoError(t, heightsync.SignAck(validator, duplicate))
		h.HostMempool().AddTx(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: duplicate}})
		proof, err := producer.BuildMainnetHeightTx(t.Context())
		require.NoError(t, err)
		txs := []*types.DevshardTx{proof, duringStream}
		root, err := producer.ApplyLocal(3, txs)
		require.NoError(t, err)
		committed, err := DiffToJSON(testutil.SignDiffWithRoot(t, owner, escrowID, 3, txs, root))
		require.NoError(t, err)
		committedBody, err := json.Marshal(InferenceRequest{Nonce: 3, Diffs: []DiffJSON{committed}, Stream: true})
		require.NoError(t, err)
		_, nextAnchor := collectChatFrames(t, &serverTestEnv{server: srv, userSigner: owner}, committedBody)
		require.Contains(t, nextAnchor, `"height_sync"`, "the accepted slot cannot block the next anchor")
	}
}

func TestResponseAnchorKeepsRelayedAcksIndependent(t *testing.T) {
	ack := func(slot uint32, height uint64) *types.DevshardTx {
		return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
			SlotId: slot, ObservedHeight: height, ObservedBlockHash: []byte{1},
		}}}
	}
	own, obsolete, relayed, later := ack(0, 100), ack(0, 99), ack(1, 99), ack(0, 101)
	slots := map[uint32]bool{0: true}
	section := &heightsync.HeightSyncSection{MainnetHeight: 100, ProofType: heightsync.AnchorProofType, MainnetBlockHashHex: "01"}
	paired := responseHeightAcks(section, []*types.DevshardTx{own}, []*types.DevshardTx{obsolete, relayed}, slots)
	require.Equal(t, map[*types.DevshardTx]struct{}{own: {}}, paired)
	require.True(t, responseAnchorCompatibleWithAcks(section, []*types.DevshardTx{own}))
	lower := *section
	lower.MainnetHeight--
	require.False(t, responseAnchorCompatibleWithAcks(&lower, []*types.DevshardTx{own}), "a lower anchor can also cause the peer to discard a fresh ACK")
	require.Equal(t, []*types.DevshardTx{own, relayed}, retainAnchoredResponseAcks(
		[]*types.DevshardTx{own, obsolete, relayed, later}, paired, slots))
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

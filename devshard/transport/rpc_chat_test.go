package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
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

	// Same BestSpeed windowed gzip. A slide into per-frame members is a
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

func TestParseChatStream_ZeroFramesTruncated(t *testing.T) {
	c := &RPCClient{HTTPClient: &HTTPClient{config: DefaultClientConfig()}}
	_, err := c.parseChatStream(context.Background(), nil, io.Discard, nil)
	require.ErrorIs(t, err, ErrSSEStreamTruncated)
}

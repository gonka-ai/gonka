package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

func setupClientTestEnv(t *testing.T) (*HTTPClient, *httptest.Server, *signing.Secp256k1Signer, []types.SlotAssignment) {
	t.Helper()
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", userSigner.Address(), config, group, 100000))
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       "escrow-1",
		Version:        testutil.RuntimeTestVersion,
		Config:         config,
		Group:          group,
		InitialBalance: 100000,
	}))

	h, err := host.NewHost(sm, hostSigner, engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	srv, err := NewServer(h, store, verifier, userSigner.Address())
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/devshard/v2")
	registerServer(g, srv)

	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	cfg := DefaultClientConfig()
	cfg.RoutePrefix = testRoutePrefix
	client := NewHTTPClient(ts.URL, "escrow-1", userSigner, cfg)
	return client, ts, userSigner, group
}

func TestHTTPClient_Send_RoundTrip(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnv(t)
	ctx := context.Background()

	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})

	resp, err := client.Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Nonce)
	require.NotNil(t, resp.StateSig)
	require.NotNil(t, resp.Receipt)
	require.NotEmpty(t, resp.Mempool)

	// Verify mempool contains MsgFinishInference.
	var hasFinish bool
	for _, tx := range resp.Mempool {
		if tx.GetFinishInference() != nil {
			hasFinish = true
		}
	}
	require.True(t, hasFinish, "mempool should contain MsgFinishInference")
}

func TestHTTPClient_Send_ReturnsUpstreamStatusError(t *testing.T) {
	userSigner := testutil.MustGenerateKey(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad signature", http.StatusForbidden)
	}))
	t.Cleanup(ts.Close)

	client := NewHTTPClient(ts.URL, "escrow-1", userSigner)
	_, err := client.Send(context.Background(), host.HostRequest{Nonce: 1}, nil, nil)
	require.Error(t, err)

	var statusErr *UpstreamStatusError
	require.True(t, errors.As(err, &statusErr))
	require.Equal(t, http.StatusForbidden, statusErr.StatusCode)
	require.Contains(t, statusErr.Path, "/sessions/escrow-1/chat/completions")
	require.Contains(t, statusErr.Body, "bad signature")
}

func TestHTTPClient_Send_NoPayloadUsesQueryTimeout(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := DefaultClientConfig()
	cfg.InferenceTimeout = time.Second
	cfg.QueryTimeout = 25 * time.Millisecond
	client := NewHTTPClient(srv.URL, "escrow-1", signer, cfg)

	start := time.Now()
	_, err := client.Send(context.Background(), host.HostRequest{Nonce: 1}, nil, nil)

	require.Error(t, err)
	require.Less(t, time.Since(start), 200*time.Millisecond)
}

func TestHTTPClient_GetDiffs(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnv(t)
	ctx := context.Background()

	// Send an inference to create a stored diff.
	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := client.Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)

	// Fetch diffs.
	diffs, err := client.GetDiffs(ctx, 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, uint64(1), diffs[0].Nonce)
}

func TestHTTPClient_GetMempool(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnv(t)
	ctx := context.Background()

	// Send an inference to populate mempool with MsgFinishInference.
	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := client.Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.NoError(t, err)

	// Fetch mempool.
	txs, err := client.GetMempool(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, txs)
}

func TestParseSSE_PartialResult(t *testing.T) {
	// Simulate a server that sends devshard_receipt then closes the connection.
	// parseSSEResponse should return the partial result with receipt alongside the error.
	client := &HTTPClient{config: DefaultClientConfig()}

	sseData := "data: {\"devshard_receipt\":{\"state_sig\":\"c2ln\",\"state_hash\":\"aGFzaA==\",\"nonce\":1,\"receipt\":\"cmVjZWlwdA==\",\"confirmed_at\":1000}}\n\n"
	// Use a reader that returns the data then an error (simulating connection drop).
	r := &truncatedReader{data: []byte(sseData)}

	result, err := client.parseSSEResponse(r, nil, nil)
	require.Error(t, err, "should return error from broken stream")
	require.NotNil(t, result, "should return partial result")
	require.Equal(t, uint64(1), result.Nonce)
	require.NotNil(t, result.Receipt, "receipt should be extracted from partial stream")
	require.Equal(t, int64(1000), result.ConfirmedAt)
}

// truncatedReader returns data followed by an io.ErrUnexpectedEOF to simulate a broken connection.
type truncatedReader struct {
	data []byte
	pos  int
	done bool
}

func (r *truncatedReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, fmt.Errorf("connection reset")
	}
	if r.pos >= len(r.data) {
		r.done = true
		return 0, fmt.Errorf("connection reset")
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestHTTPClient_Send_SSE(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnv(t)
	ctx := context.Background()

	var streamLines []string
	streamSink := lineCollector(func(line string) {
		streamLines = append(streamLines, line)
	})

	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := client.Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, streamSink, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Nonce)
	require.NotNil(t, resp.StateSig)
	require.NotNil(t, resp.Receipt)
	require.NotEmpty(t, resp.Mempool)

	// StreamCallback should have received inference data lines.
	require.NotEmpty(t, streamLines, "stream callback should receive inference data")

	// Verify mempool contains MsgFinishInference.
	var hasFinish bool
	for _, tx := range resp.Mempool {
		if tx.GetFinishInference() != nil {
			hasFinish = true
		}
	}
	require.True(t, hasFinish, "mempool should contain MsgFinishInference")
}

type stubAdmissionController struct {
	calls    []string
	observed []string
	err      error
}

func (s *stubAdmissionController) AllowRequest(participantKey, path string) error {
	s.calls = append(s.calls, participantKey+":"+path)
	return s.err
}

func (s *stubAdmissionController) ObserveResult(participantKey, path string, statusCode int) {
	s.observed = append(s.observed, fmt.Sprintf("%s:%s:%d", participantKey, path, statusCode))
}

func (s *stubAdmissionController) ObserveTransportFailure(participantKey, path string, err error) {
	s.observed = append(s.observed, fmt.Sprintf("%s:%s:transport", participantKey, path))
}

func TestHTTPClient_Send_UsesAdmissionController(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnv(t)
	ctx := context.Background()
	admission := &stubAdmissionController{err: fmt.Errorf("participant request budget exhausted")}
	client.config.ParticipantKey = "shared-host"
	client.config.Admission = admission

	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := client.Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.ErrorContains(t, err, "participant request budget exhausted")
	require.Len(t, admission.calls, 1)
	require.Contains(t, admission.calls[0], "shared-host")
	require.Contains(t, admission.calls[0], "/sessions/escrow-1/chat/completions")
}

func TestHTTPClient_Send_ObservesUpstream503(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	admission := &stubAdmissionController{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("nginx limit"))
	}))
	t.Cleanup(server.Close)

	client := NewHTTPClient(server.URL, "escrow-1", signer, ClientConfig{
		InferenceTimeout: DefaultClientConfig().InferenceTimeout,
		GossipTimeout:    DefaultClientConfig().GossipTimeout,
		VerifyTimeout:    DefaultClientConfig().VerifyTimeout,
		QueryTimeout:     DefaultClientConfig().QueryTimeout,
		ParticipantKey:   "shared-host",
		Admission:        admission,
	})

	_, err := client.Send(context.Background(), host.HostRequest{
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}, nil, nil)
	require.Error(t, err)
	var upstreamErr *UpstreamStatusError
	require.ErrorAs(t, err, &upstreamErr)
	require.Equal(t, http.StatusServiceUnavailable, upstreamErr.StatusCode)
	require.Len(t, admission.observed, 1)
	require.Contains(t, admission.observed[0], "shared-host")
	require.Contains(t, admission.observed[0], ":503")
}

type lineCollector func(line string)

func (c lineCollector) Write(p []byte) (int, error) {
	c(string(p))
	return len(p), nil
}

package transport

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/blockoracle"
	"devshard/heightsync"
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

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 100000}))

	h, err := host.NewHost(sm, hostSigner, engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	srv, err := NewServer(h, store, verifier, userSigner.Address())
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/v1/devshard")
	srv.Register(g)

	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	client := NewHTTPClient(ts.URL, "escrow-1", userSigner)
	return client, ts, userSigner, group
}

// setupClientTestEnvWithHeightSync is like setupClientTestEnv but wires the same
// AnchorScheduler on both transport.Server (host) and HTTPClient (user).
func setupClientTestEnvWithHeightSync(t *testing.T) (*HTTPClient, *httptest.Server, *signing.Secp256k1Signer, *heightSyncTestOracle) {
	t.Helper()
	hostSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 100000}))

	h, err := host.NewHost(sm, hostSigner, engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	or := &heightSyncTestOracle{hdr: &blockoracle.Header{
		Height:    42,
		ChainID:   "test-chain",
		BlockHash: []byte{0x01, 0x02},
	}}
	// Separate schedulers: host and user must not share one AnchorScheduler instance.
	hostSched := heightsync.MustNewAnchorScheduler(10, 1, or)
	clientSched := heightsync.MustNewAnchorScheduler(10, 1, or)

	srv, err := NewServer(h, store, verifier, userSigner.Address(), WithHeightSync(hostSched, or))
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/v1/devshard")
	srv.Register(g)

	ts := httptest.NewServer(e)
	t.Cleanup(ts.Close)

	cfg := DefaultClientConfig()
	cfg.HeightSync = clientSched
	cfg.HeightSyncLogOracle = or
	client := NewHTTPClient(ts.URL, "escrow-1", userSigner, cfg)
	return client, ts, userSigner, or
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
	})
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

func TestHTTPClient_Send_HeightSync_ProtobufRequestAndAudit(t *testing.T) {
	client, _, userSigner, _ := setupClientTestEnvWithHeightSync(t)
	ctx := context.Background()

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
	})
	require.NoError(t, err)

	ring := client.HeightSyncAuditRing()
	require.NotNil(t, ring)
	userAddr := userSigner.Address()
	foundReq := false
	foundResp := false
	for _, a := range ring.List(userAddr) {
		if a.Direction == "request" && a.MainnetHeight == 42 {
			foundReq = true
		}
	}
	for _, a := range ring.List(client.baseURL) {
		if a.Direction == "response" && a.MainnetHeight == 42 {
			foundResp = true
		}
	}
	require.True(t, foundReq, "expected outbound user anchor in audit ring")
	require.True(t, foundResp, "expected inbound host anchor from SSE in audit ring")
}

func TestHostRequest_ForceHeightSyncAnchor_TransportJSONRoundTrip(t *testing.T) {
	hr := host.HostRequest{
		Diffs:                 nil,
		Nonce:                 7,
		ForceHeightSyncAnchor: true,
	}
	ir, err := HostRequestToJSON(hr)
	require.NoError(t, err)
	require.True(t, ir.ForceHeightSyncAnchor)
	hr2, err := HostRequestFromJSON(ir)
	require.NoError(t, err)
	require.True(t, hr2.ForceHeightSyncAnchor)
	require.Equal(t, uint64(7), hr2.Nonce)
}

func TestHTTPClient_ParseSSE_InboundHeightSyncAudit(t *testing.T) {
	cfg := DefaultClientConfig()
	client := &HTTPClient{
		config:          cfg,
		baseURL:         "http://executor-host",
		heightSyncAudit: heightsync.NewAuditRing(4),
	}
	sse := `data: {"devshard_receipt":{"state_sig":"c2ln","state_hash":"aGFzaA==","nonce":1,"receipt":"cmVjZWlwdA==","confirmed_at":1000},"height_sync":{"proof_type":"height-anchor-v1","mainnet_height":99,"mainnet_block_hash_hex":"aabb","timestamp_unix_ms":1,"direction":"response"}}` + "\n\n"

	res, err := client.parseSSEResponse(strings.NewReader(sse), 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), res.Nonce)

	var saw bool
	for _, a := range client.HeightSyncAuditRing().List("http://executor-host") {
		if a.Direction == "response" && a.MainnetHeight == 99 {
			saw = true
		}
	}
	require.True(t, saw)
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
	})
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
	})
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

	result, err := client.parseSSEResponse(r, 1)
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

	// Configure a stream callback to collect data lines.
	var streamLines []string
	client.config.StreamCallback = func(nonce uint64, line string) {
		streamLines = append(streamLines, line)
	}

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
	})
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

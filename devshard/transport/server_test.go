package transport

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/blockoracle"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

type serverTestEnv struct {
	server     *Server
	echo       *echo.Echo
	store      *storage.Memory
	userSigner *signing.Secp256k1Signer
	hostSigner *signing.Secp256k1Signer
	group      []types.SlotAssignment
	config     types.SessionConfig
}

func setupServerEnv(t *testing.T, opts ...ServerOption) *serverTestEnv {
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

	srv, err := NewServer(h, store, verifier, userSigner.Address(), opts...)
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/v1/devshard")
	srv.Register(g)

	return &serverTestEnv{
		server:     srv,
		echo:       e,
		store:      store,
		userSigner: userSigner,
		hostSigner: hostSigner,
		group:      group,
		config:     config,
	}
}

func (env *serverTestEnv) doPost(t *testing.T, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()

	ts := time.Now().Unix()
	sig, err := SignRequest(env.userSigner, "escrow-1", body, ts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func (env *serverTestEnv) doPostContentType(t *testing.T, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now().Unix()
	sig, err := SignRequest(env.userSigner, "escrow-1", body, ts)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func (env *serverTestEnv) doGet(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func TestServer_Inference_ValidAuth(t *testing.T) {
	env := setupServerEnv(t)

	// Build a valid inference request.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)

	ir := InferenceRequest{
		Diffs: []DiffJSON{dj},
		Nonce: 1,
		Payload: &PayloadJSON{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	}
	body, err := json.Marshal(ir)
	require.NoError(t, err)

	rec := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	// Parse SSE events.
	var receipt DevshardReceiptEvent
	var meta DevshardMetaEvent
	lines := strings.Split(rec.Body.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if raw, ok := envelope["devshard_receipt"]; ok {
			require.NoError(t, json.Unmarshal(raw, &receipt))
		}
		if raw, ok := envelope["devshard_meta"]; ok {
			require.NoError(t, json.Unmarshal(raw, &meta))
		}
	}

	require.Equal(t, uint64(1), receipt.Nonce)
	require.NotNil(t, receipt.StateSig)
	require.NotNil(t, receipt.Receipt) // single host is always executor
	require.NotEmpty(t, meta.Mempool)
}

func TestServer_Inference_NoAuth(t *testing.T) {
	env := setupServerEnv(t)

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/devshard/sessions/escrow-1/chat/completions",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServer_Inference_NotInGroup(t *testing.T) {
	env := setupServerEnv(t)

	outsider := testutil.MustGenerateKey(t)
	body := []byte(`{}`)
	ts := time.Now().Unix()
	sig, err := SignRequest(outsider, "escrow-1", body, ts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/devshard/sessions/escrow-1/chat/completions",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServer_GetDiffs(t *testing.T) {
	env := setupServerEnv(t)

	// First apply a diff via the inference endpoint.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000},
	}
	body, _ := json.Marshal(ir)
	rec := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code)

	// Now GET diffs.
	rec = env.doGet(t, "/v1/devshard/sessions/escrow-1/diffs?from=1&to=1")
	require.Equal(t, http.StatusOK, rec.Code)

	var diffs []json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &diffs))
	require.Len(t, diffs, 1)
}

func TestServer_GetMempool(t *testing.T) {
	env := setupServerEnv(t)

	// Apply a diff to populate the mempool with MsgFinishInference.
	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000},
	}
	body, _ := json.Marshal(ir)
	rec := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code)

	// GET mempool.
	rec = env.doGet(t, "/v1/devshard/sessions/escrow-1/mempool")
	require.Equal(t, http.StatusOK, rec.Code)

	var result struct {
		Txs [][]byte `json:"txs"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.NotEmpty(t, result.Txs)
}

func TestServer_RateLimit(t *testing.T) {
	env := setupServerEnv(t)

	// Re-create server with a tight rate limit.
	srv, err := NewServer(env.server.host, env.store,
		env.server.verifier, env.userSigner.Address(),
		WithRateLimit(RateLimitConfig{RequestsPerSecond: 1, BurstSize: 1}))
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/v1/devshard")
	srv.Register(g)

	body := []byte(`{}`)
	doReq := func() int {
		ts := time.Now().Unix()
		sig, _ := SignRequest(env.userSigner, "escrow-1", body, ts)
		req := httptest.NewRequest(http.MethodPost, "/v1/devshard/sessions/escrow-1/chat/completions",
			strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
		req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	// First request should pass (burst=1).
	code := doReq()
	// Could be 200 or 400 (bad inference body), but not 429.
	require.NotEqual(t, http.StatusTooManyRequests, code)

	// Second request should be rate limited.
	code = doReq()
	require.Equal(t, http.StatusTooManyRequests, code)
}

func TestHandleGossipNonce_WarmKey(t *testing.T) {
	// Set up: host signer at slot 0, warm key for slot 0.
	hostSigner := testutil.MustGenerateKey(t)
	warmSigner := testutil.MustGenerateKey(t)
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostSigner})
	config := testutil.DefaultConfig(1)
	verifier := signing.NewSecp256k1Verifier()

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hostSigner.Address(), nil
	}

	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, state.WithWarmKeyResolver(resolver))
	require.NoError(t, err)

	// Create warm key binding via confirm start.
	diff1 := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	_, err = sm.ApplyDiff(diff1)
	require.NoError(t, err)

	// inference 1 % 1 = 0, executor = slot 0.
	execSig := testutil.SignExecutorReceipt(t, warmSigner, "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 1000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	}}}
	diff2 := testutil.SignDiff(t, userSigner, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	_, err = sm.ApplyDiff(diff2)
	require.NoError(t, err)

	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 100000}))

	// Rebuild SM from scratch for host (host needs nonce 0 start).
	sm2, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier, state.WithWarmKeyResolver(resolver))
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	h, err := host.NewHost(sm2, hostSigner, engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store), host.WithVerifier(verifier))
	require.NoError(t, err)

	srv, err := NewServer(h, store, verifier, userSigner.Address())
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/v1/devshard")
	srv.Register(g)

	// Apply diffs through the host to populate storage.
	_, err = h.HandleRequest(context.Background(), host.HostRequest{Diffs: []types.Diff{diff1, diff2}})
	require.NoError(t, err)

	// Compute state root for signing.
	stateRoot, err := h.StateRoot()
	require.NoError(t, err)

	// Sign state with warm key.
	sigContent := &types.StateSignatureContent{
		StateRoot: stateRoot,
		EscrowId:  "escrow-1",
		Nonce:     2,
	}
	sigData, merr := proto.Marshal(sigContent)
	require.NoError(t, merr)
	warmStateSig, err := warmSigner.Sign(sigData)
	require.NoError(t, err)

	// Build gossip nonce request.
	nonceReq := GossipNonceRequest{
		Nonce:     2,
		StateHash: stateRoot,
		StateSig:  warmStateSig,
		SlotID:    0,
	}
	body, err := json.Marshal(nonceReq)
	require.NoError(t, err)

	// Sign the HTTP request with warm key (warm key is a group member via bridge).
	ts := time.Now().Unix()
	sig, err := SignRequest(warmSigner, "escrow-1", body, ts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/devshard/sessions/escrow-1/gossip/nonce", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "warm key gossip nonce should succeed, got: %s", rec.Body.String())
}

func TestServer_StreamingInference(t *testing.T) {
	env := setupServerEnv(t)

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)

	ir := InferenceRequest{
		Diffs: []DiffJSON{dj},
		Nonce: 1,
		Payload: &PayloadJSON{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
		Stream: true,
	}
	body, err := json.Marshal(ir)
	require.NoError(t, err)

	rec := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	// Parse all SSE events.
	var hasReceipt, hasMeta, hasInferenceData bool
	lines := strings.Split(rec.Body.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			hasInferenceData = true
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if _, ok := envelope["devshard_receipt"]; ok {
			hasReceipt = true
		}
		if _, ok := envelope["devshard_meta"]; ok {
			hasMeta = true
		}
		if _, ok := envelope["choices"]; ok {
			hasInferenceData = true
		}
	}
	require.True(t, hasReceipt, "should have devshard_receipt event")
	require.True(t, hasMeta, "should have devshard_meta event")
	require.True(t, hasInferenceData, "should have inference data events")
}

func (env *serverTestEnv) doPostAs(t *testing.T, path string, body []byte, signer *signing.Secp256k1Signer) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now().Unix()
	sig, err := SignRequest(signer, "escrow-1", body, ts)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func TestServer_Inference_GroupMemberRejected(t *testing.T) {
	env := setupServerEnv(t)
	body := []byte(`{}`)
	rec := env.doPostAs(t, "/v1/devshard/sessions/escrow-1/chat/completions", body, env.hostSigner)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServer_VerifyTimeout_GroupMemberRejected(t *testing.T) {
	env := setupServerEnv(t)
	body := []byte(`{}`)
	rec := env.doPostAs(t, "/v1/devshard/sessions/escrow-1/verify-timeout", body, env.hostSigner)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServer_ChallengeReceipt_GroupMemberAllowed(t *testing.T) {
	env := setupServerEnv(t)
	// Group members (peer hosts) must be allowed to call ChallengeReceipt
	// during timeout verification. Empty diffs + no matching inference = 200 with empty receipt.
	body := []byte(`{"inference_id":999,"diffs":[],"payload":null}`)
	rec := env.doPostAs(t, "/v1/devshard/sessions/escrow-1/challenge-receipt", body, env.hostSigner)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestServer_NonExecutor_SSE(t *testing.T) {
	// 3 hosts, request to non-executor.
	hostSigners := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(3)
	verifier := signing.NewSecp256k1Verifier()

	// Host at slot 0. Inference 1 maps to executor slot 1, so host 0 is NOT executor.
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, userSigner.Address(), verifier)
	require.NoError(t, err)
	engine := stub.NewInferenceEngine()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 100000}))

	h, err := host.NewHost(sm, hostSigners[0], engine, "escrow-1", group, nil, host.WithGrace(100), host.WithStorage(store))
	require.NoError(t, err)

	srv, err := NewServer(h, store, verifier, userSigner.Address())
	require.NoError(t, err)

	e := echo.New()
	g := e.Group("/v1/devshard")
	srv.Register(g)

	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000},
	}
	body, _ := json.Marshal(ir)

	ts := require.New(t)
	reqTime := time.Now().Unix()
	sig, sigErr := SignRequest(userSigner, "escrow-1", body, reqTime)
	ts.NoError(sigErr)

	req := httptest.NewRequest(http.MethodPost, "/v1/devshard/sessions/escrow-1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", reqTime))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	// Parse events: should have receipt but no inference data (not executor).
	var receipt DevshardReceiptEvent
	var hasInferenceData bool
	lines := strings.Split(rec.Body.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			hasInferenceData = true
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if raw, ok := envelope["devshard_receipt"]; ok {
			json.Unmarshal(raw, &receipt)
		}
		if _, ok := envelope["choices"]; ok {
			hasInferenceData = true
		}
	}

	require.Nil(t, receipt.Receipt, "non-executor should not have receipt")
	require.False(t, hasInferenceData, "non-executor should not have inference data")
}

// heightSyncTestOracle implements blockoracle.BlockOracle for transport tests.
type heightSyncTestOracle struct {
	hdr *blockoracle.Header
}

func (o *heightSyncTestOracle) Latest(context.Context) (*blockoracle.Header, error) {
	if o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *heightSyncTestOracle) At(context.Context, int64) (*blockoracle.Header, error) { return nil, nil }

func (o *heightSyncTestOracle) Prove(context.Context, string, int64) (*blockoracle.Proof, error) {
	return nil, nil
}

func (o *heightSyncTestOracle) Subscribe(context.Context, int64) (<-chan *blockoracle.Header, error) {
	ch := make(chan *blockoracle.Header)
	close(ch)
	return ch, nil
}

func TestServer_Inference_HeightSync_OutboundAnchor(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blockoracle.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	sched := heightsync.MustNewAnchorScheduler(10, 1, or)

	env := setupServerEnv(t, WithHeightSync(sched, or))

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000},
	}
	body, err := json.Marshal(ir)
	require.NoError(t, err)

	rec := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var hs heightsync.HeightSyncSection
	foundHS := false
	lines := strings.Split(rec.Body.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if raw, ok := envelope["height_sync"]; ok {
			require.NoError(t, json.Unmarshal(raw, &hs))
			foundHS = true
			break
		}
	}
	require.True(t, foundHS, "expected height_sync on first SSE receipt")
	require.Equal(t, heightsync.AnchorProofType, hs.ProofType)
	require.Equal(t, int64(77), hs.MainnetHeight)
	require.Equal(t, "response", hs.Direction)

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	localAddr := env.hostSigner.Address()
	var sawResponse bool
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 77 {
			sawResponse = true
			break
		}
	}
	require.True(t, sawResponse, "expected outbound anchor in audit ring")
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 77 {
			require.Equal(t, heightsync.TrustOracle, a.Trust)
			break
		}
	}
}

func sseFirstReceiptHasHeightSync(body string) bool {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			continue
		}
		if _, ok := envelope["devshard_receipt"]; !ok {
			continue
		}
		_, has := envelope["height_sync"]
		return has
	}
	return false
}

// TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest covers plan §9.3 item 6 (host side):
// InferenceRequest.force_height_sync_anchor drives DecideHints.ForceAnchor on the outbound receipt
// so the host emits Anchor even when responseNonce falls outside the sync-turn cadence.
func TestServer_Inference_HeightSync_ForceAnchor_OnInferenceRequest(t *testing.T) {
	or := &heightSyncTestOracle{hdr: &blockoracle.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: []byte{0xab, 0xcd},
	}}
	sched := heightsync.MustNewAnchorScheduler(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	payload := &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000}

	diff1 := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj1, err := DiffToJSON(diff1)
	require.NoError(t, err)
	ir1 := InferenceRequest{Diffs: []DiffJSON{dj1}, Nonce: 1, Payload: payload}
	body1, err := json.Marshal(ir1)
	require.NoError(t, err)
	rec1 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body1)
	require.Equal(t, http.StatusOK, rec1.Code, rec1.Body.String())
	require.True(t, sseFirstReceiptHasHeightSync(rec1.Body.String()), "responseNonce=1 is inside initial sync turn (slots=1)")

	txs2 := env.server.Host().MempoolTxs()
	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, txs2)
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)
	ir2 := InferenceRequest{Diffs: []DiffJSON{dj2}, Nonce: 2, Payload: payload}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)
	rec2 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	require.False(t, sseFirstReceiptHasHeightSync(rec2.Body.String()),
		"responseNonce=2 should Omit without force_height_sync_anchor (K=10, slots=1)")

	txs3 := env.server.Host().MempoolTxs()
	diff3 := testutil.SignDiff(t, env.userSigner, "escrow-1", 3, txs3)
	dj3, err := DiffToJSON(diff3)
	require.NoError(t, err)
	ir3 := InferenceRequest{
		Diffs:                 []DiffJSON{dj3},
		Nonce:                 3,
		ForceHeightSyncAnchor: true,
		Payload:               payload,
	}
	body3, err := json.Marshal(ir3)
	require.NoError(t, err)
	rec3 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body3)
	require.Equal(t, http.StatusOK, rec3.Code, rec3.Body.String())
	require.True(t, sseFirstReceiptHasHeightSync(rec3.Body.String()),
		"force_height_sync_anchor must emit Anchor on responseNonce=3")

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	localAddr := env.hostSigner.Address()
	nResp := 0
	for _, a := range ring.List(localAddr) {
		if a.Direction == "response" && a.MainnetHeight == 77 {
			nResp++
		}
	}
	require.GreaterOrEqual(t, nResp, 2, "expect anchors at least for cadence response 1 and forced response 3")
}

type discardRestLogger struct{}

func (discardRestLogger) Info(string, ...any)  {}
func (discardRestLogger) Error(string, ...any) {}
func (discardRestLogger) Warn(string, ...any)  {}
func (discardRestLogger) Debug(string, ...any) {}

type warnCaptureLogger struct {
	warns []string
	discardRestLogger
}

func (w *warnCaptureLogger) Warn(msg string, kv ...any) {
	w.warns = append(w.warns, msg)
}

type mutableTestOracle struct {
	mu  sync.Mutex
	hdr *blockoracle.Header
}

func (o *mutableTestOracle) Latest(context.Context) (*blockoracle.Header, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hdr == nil {
		return nil, nil
	}
	h := *o.hdr
	h.BlockHash = append([]byte(nil), o.hdr.BlockHash...)
	return &h, nil
}

func (o *mutableTestOracle) At(context.Context, int64) (*blockoracle.Header, error) { return nil, nil }

func (o *mutableTestOracle) Prove(context.Context, string, int64) (*blockoracle.Proof, error) {
	return nil, nil
}

func (o *mutableTestOracle) Subscribe(context.Context, int64) (<-chan *blockoracle.Header, error) {
	ch := make(chan *blockoracle.Header)
	close(ch)
	return ch, nil
}

func TestServer_Inference_HeightSync_UntrustedReconcileMismatchWarns(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	or := &mutableTestOracle{hdr: &blockoracle.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorScheduler(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000},
	}
	peerHash := hex.EncodeToString(bytes.Repeat([]byte{0xbb}, 32))
	hs := &heightsync.HeightSyncSection{
		ChainID:               "chain-x",
		ProofType:             heightsync.AnchorProofType,
		MainnetHeight:         11,
		MainnetBlockHashHex:   peerHash,
		TimestampUnixMs:       time.Now().UnixMilli(),
		Direction:             "request",
	}
	wrapBody, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)

	rec := env.doPostContentType(t, "/v1/devshard/sessions/escrow-1/chat/completions", "application/x-protobuf", wrapBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, capLog.warns)

	ring := env.server.HeightSyncAuditRing()
	userAddr := env.userSigner.Address()
	var sawUntrusted bool
	for _, a := range ring.List(userAddr) {
		if a.Direction == "request" && a.MainnetHeight == 11 {
			require.Equal(t, heightsync.TrustUntrustedPeer, a.Trust)
			sawUntrusted = true
			break
		}
	}
	require.True(t, sawUntrusted)

	or.mu.Lock()
	or.hdr.Height = 11
	or.hdr.BlockHash = bytes.Repeat([]byte{0xcc}, 32)
	or.mu.Unlock()

	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, nil)
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)
	ir2 := InferenceRequest{
		Diffs:   []DiffJSON{dj2},
		Nonce:   2,
		Payload: ir.Payload,
		Stream:  ir.Stream,
	}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)
	rec2 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code)

	require.NotEmpty(t, capLog.warns)
	require.Contains(t, capLog.warns[0], "untrusted peer tip disagrees")
}

func TestServer_Inference_HeightSync_UntrustedReconcileMatchNoWarn(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	matchHash := bytes.Repeat([]byte{0xbb}, 32)
	or := &mutableTestOracle{hdr: &blockoracle.Header{
		Height:    10,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0x01}, 32),
	}}
	sched := heightsync.MustNewAnchorScheduler(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	diff := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	ir := InferenceRequest{
		Diffs:   []DiffJSON{dj},
		Nonce:   1,
		Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000},
	}
	peerHash := hex.EncodeToString(matchHash)
	hs := &heightsync.HeightSyncSection{
		ChainID:             "chain-x",
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       11,
		MainnetBlockHashHex: peerHash,
		Direction:           "request",
	}
	wrapBody, err := MarshalWrappedInferenceRequest(CurrentInferenceEnvelopeSchemaVersion, hs, ir)
	require.NoError(t, err)
	rec := env.doPostContentType(t, "/v1/devshard/sessions/escrow-1/chat/completions", "application/x-protobuf", wrapBody)
	require.Equal(t, http.StatusOK, rec.Code)

	or.mu.Lock()
	or.hdr.Height = 11
	or.hdr.BlockHash = append([]byte(nil), matchHash...)
	or.mu.Unlock()

	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, nil)
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)
	ir2 := InferenceRequest{
		Diffs:   []DiffJSON{dj2},
		Nonce:   2,
		Payload: ir.Payload,
		Stream:  ir.Stream,
	}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)
	rec2 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code)
	require.Empty(t, capLog.warns)
}

// TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits
// covers the malicious-user variant of the forced sync turn (plan §5.5,
// SCENARIOS.md "Manual-force forced sync turn — Scenario E"):
//
//   - The trigger diff carries MsgForceHeightSyncTurn at nonce 2; the host
//     applies it via catch-up and its escrow state opens a forced window.
//   - The user's HTTP envelope at nonce 2 is plain JSON (no height_sync).
//   - The server MUST still process the request and emit Anchor on the SSE
//     receipt (response side is normatively bound by the host's own state).
//   - The server MUST log a "heightsync: force_request_anchor_missing" warn
//     entry and append a TrustForceRequestAnchorMissing audit-ring entry
//     against the inbound peer id (request side is best-effort + dispute
//     evidence, not a rejection).
func TestServer_Inference_HeightSync_ForcedTurn_HostAnchorsEvenIfRequestOmits(t *testing.T) {
	capLog := &warnCaptureLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(discardRestLogger{}) })

	or := &heightSyncTestOracle{hdr: &blockoracle.Header{
		Height:    77,
		ChainID:   "chain-x",
		BlockHash: bytes.Repeat([]byte{0xa1}, 32),
	}}
	// K=10, slots=1: cadence anchors at nonces 1 and 10. Nonce 2 would
	// normally Omit, so any Anchor at nonce 2 is attributable to the
	// forced turn alone.
	sched := heightsync.MustNewAnchorScheduler(10, 1, or)
	env := setupServerEnv(t, WithHeightSync(sched, or))

	payload := &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: 50, StartedAt: 1000}

	diff1 := testutil.SignDiff(t, env.userSigner, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	dj1, err := DiffToJSON(diff1)
	require.NoError(t, err)
	ir1 := InferenceRequest{Diffs: []DiffJSON{dj1}, Nonce: 1, Payload: payload}
	body1, err := json.Marshal(ir1)
	require.NoError(t, err)
	rec1 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body1)
	require.Equal(t, http.StatusOK, rec1.Code, rec1.Body.String())

	require.False(t, env.server.host.HeightSyncEscrowHints(10, 1) != nil &&
		env.server.host.HeightSyncEscrowHints(10, 1).ForcedEnd != 0,
		"sanity: no forced turn before trigger diff is applied")

	// Trigger diff at nonce 2: MsgForceHeightSyncTurn opens window [2,2]
	// (single-host group → slots=1). The matching StartInference covers the
	// inference path so the request still produces a normal receipt.
	forceTx := &types.DevshardTx{Tx: &types.DevshardTx_ForceHeightSyncTurn{
		ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{
			TriggerNonce: 2,
			EndNonce:     2,
			AnchorK:      10,
			SlotsNum:     1,
			Reason:       "test_malicious_user",
		},
	}}
	startTx2 := testutil.StartTx(2)
	diff2 := testutil.SignDiff(t, env.userSigner, "escrow-1", 2, []*types.DevshardTx{forceTx, startTx2})
	dj2, err := DiffToJSON(diff2)
	require.NoError(t, err)

	// Plain-JSON envelope — no height_sync section. Simulates a malicious
	// user that signed the trigger diff but strips the Anchor on the wire.
	ir2 := InferenceRequest{Diffs: []DiffJSON{dj2}, Nonce: 2, Payload: payload}
	body2, err := json.Marshal(ir2)
	require.NoError(t, err)

	rec2 := env.doPost(t, "/v1/devshard/sessions/escrow-1/chat/completions", body2)
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())

	require.True(t, sseFirstReceiptHasHeightSync(rec2.Body.String()),
		"host response at nonce=2 inside forced window MUST Anchor regardless of user's missing inbound Anchor")

	postH := env.server.host.HeightSyncEscrowHints(10, 1)
	require.NotNil(t, postH, "forced turn must be live in escrow state after diff applies")
	require.Equal(t, uint64(2), postH.ForcedStart)
	require.Equal(t, uint64(2), postH.ForcedEnd)

	require.Contains(t, capLog.warns, "heightsync: force_request_anchor_missing",
		"server must log a warn entry when an in-window request omits height_sync")

	ring := env.server.HeightSyncAuditRing()
	require.NotNil(t, ring)
	userAddr := env.userSigner.Address()
	var sawMissing bool
	for _, a := range ring.List(userAddr) {
		if a.Trust == heightsync.TrustForceRequestAnchorMissing && a.Direction == "request" {
			require.Zero(t, a.MainnetHeight, "missing-anchor sentinel must carry no oracle data")
			require.Empty(t, a.MainnetBlockHash, "missing-anchor sentinel must carry no oracle data")
			sawMissing = true
			break
		}
	}
	require.True(t, sawMissing,
		"audit ring must contain a TrustForceRequestAnchorMissing entry for the in-window request")
}

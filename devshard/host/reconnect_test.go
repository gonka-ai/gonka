package host

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/storage/payloads"

	"devshard"
	"devshard/internal/testutil"
	"devshard/observability"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type blockingStreamEngine struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingStreamEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	if req.ResponseWriter != nil {
		_, _ = req.ResponseWriter.Write([]byte(`data: {"choices":[{"delta":{"content":"Hi"}}]}` + "\n"))
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
	close(e.started)
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	body := []byte(`{"events":["data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}","data: [DONE]"]}`)
	return &devshard.ExecuteResult{
		ResponseHash: []byte("h"),
		InputTokens:  3,
		OutputTokens: 1,
		ResponseBody: body,
	}, nil
}

func TestHost_LiveAttach_SameNonceReconnect(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	engine := &blockingStreamEngine{started: make(chan struct{}), release: make(chan struct{})}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10),
		WithLiveStreamSpoolDir(t.TempDir()))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)

	primary := newFlushRecorder()
	resp.ExecutionJob.ResponseWriter = primary

	execDone := make(chan error, 1)
	go func() {
		_, err := h.RunExecution(context.Background(), resp.ExecutionJob)
		execDone <- err
	}()

	select {
	case <-engine.started:
	case <-time.After(2 * time.Second):
		t.Fatal("engine never started")
	}
	require.NotNil(t, h.LiveStreamForTest(1))
	h.mu.Lock()
	_, midFlightCached := h.completedResponses[1]
	h.mu.Unlock()
	require.False(t, midFlightCached, "must not publish completedResponses mid-flight")

	resp2, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.True(t, resp2.LiveAttach)
	require.NotNil(t, resp2.Receipt)
	h.mu.Lock()
	_, stillMidFlight := h.completedResponses[1]
	h.mu.Unlock()
	require.False(t, stillMidFlight, "live-attach reconnect must not publish completedResponses")

	confirmCount := 0
	for _, tx := range h.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == 1 {
			confirmCount++
		}
	}
	require.Equal(t, 1, confirmCount, "repeat ConfirmStart must be a no-op")

	attachDone := make(chan error, 1)
	rec := newFlushRecorder()
	go func() {
		attachDone <- h.AttachLiveStream(1, rec, 0, 0)
	}()
	// First post-reconnect bytes before ML finishes.
	deadline := time.After(500 * time.Millisecond)
	for rec.bodyLen() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected live attach bytes before ML completion")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	close(engine.release)
	require.NoError(t, <-attachDone)
	require.NoError(t, <-execDone)
	require.Nil(t, h.LiveStreamForTest(1), "live buffer forgotten after RunExecution")
	h.mu.Lock()
	_, cached := h.completedResponses[1]
	h.mu.Unlock()
	require.True(t, cached, "body must be in completedResponses after drain")
}

func TestHost_PostDrainReconnectUsesCache(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	body := []byte(`{"events":["data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}","data: [DONE]"]}`)
	engine := &stub.ConfigurableEngine{
		Default: devshard.ExecuteResult{
			ResponseHash: []byte("hash"),
			InputTokens:  3,
			OutputTokens: 1,
			ResponseBody: body,
		},
	}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)
	require.Nil(t, h.LiveStreamForTest(1))

	resp2, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs:            []types.Diff{diff},
		Nonce:            1,
		Payload:          defaultPayload(),
		DeliveredEvents:  1,
		DeliveredPartial: 0,
	})
	require.NoError(t, err)
	require.False(t, resp2.LiveAttach)
	require.Equal(t, body, resp2.CachedResponseBody)
	require.Equal(t, int64(1), resp2.DeliveredEvents)
}

// memPayloadStore is a tiny in-memory PayloadRetriever for resume-tier tests.
type memPayloadStore struct {
	byKey map[string][]byte
}

func (m *memPayloadStore) key(escrowID string, inferenceID, epochID uint64) string {
	return fmt.Sprintf("%s/%d/%d", escrowID, inferenceID, epochID)
}

func (m *memPayloadStore) Store(escrowID string, inferenceID, epochID uint64, response []byte) {
	if m.byKey == nil {
		m.byKey = make(map[string][]byte)
	}
	m.byKey[m.key(escrowID, inferenceID, epochID)] = append([]byte(nil), response...)
}

func (m *memPayloadStore) Retrieve(ctx context.Context, escrowID string, inferenceID, epochID uint64) ([]byte, []byte, error) {
	body, ok := m.byKey[m.key(escrowID, inferenceID, epochID)]
	if !ok {
		return nil, nil, payloads.ErrNotFound
	}
	return []byte("prompt"), body, nil
}

func TestHost_PostEvictionReconnectUsesPayloadStore(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	body := []byte(`{"events":["data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}","data: [DONE]"]}`)
	engine := &stub.ConfigurableEngine{
		Default: devshard.ExecuteResult{
			ResponseHash: []byte("hash"),
			InputTokens:  3,
			OutputTokens: 1,
			ResponseBody: body,
		},
	}
	store := &memPayloadStore{}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10), WithPayloadRetriever(store), WithEpochID(7))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)

	// Persist what the engine would have stored, then evict the RAM cache via Finish.
	store.Store("escrow-1", 1, 7, body)
	finishTx := findMempoolFinish(h.MempoolTxs())
	require.NotNil(t, finishTx)
	confirmTx := findMempoolConfirm(h.MempoolTxs())
	require.NotNil(t, confirmTx)
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{finishTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff2, diff3},
	})
	require.NoError(t, err)
	h.mu.Lock()
	_, stillCached := h.completedResponses[1]
	h.mu.Unlock()
	require.False(t, stillCached, "RAM cache must be evicted after Finish")

	resp2, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs:            []types.Diff{diff},
		Nonce:            1,
		Payload:          defaultPayload(),
		DeliveredEvents:  1,
		DeliveredPartial: 0,
	})
	require.NoError(t, err)
	require.False(t, resp2.LiveAttach)
	require.Equal(t, body, resp2.CachedResponseBody,
		"post-eviction reconnect must resume from the payload store")
	require.Equal(t, observability.ReasonCachedResponse, resp2.ReceiptReason)
	require.Nil(t, resp2.ExecutionJob)
}

func TestRetrievePayloadResponse_TriesAdjacentEpochs(t *testing.T) {
	store := &memPayloadStore{}
	store.Store("escrow-1", 9, 4, []byte("from-adj"))
	_, body, err := retrievePayloadResponse(context.Background(), store, "escrow-1", 9, 5)
	require.NoError(t, err)
	require.Equal(t, []byte("from-adj"), body)
}

func TestHost_PostEvictionWithoutPayloadStoreDisappears(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)

	body := []byte(`{"events":["data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}","data: [DONE]"]}`)
	engine := &stub.ConfigurableEngine{
		Default: devshard.ExecuteResult{
			ResponseHash: []byte("hash"),
			InputTokens:  3,
			OutputTokens: 1,
			ResponseBody: body,
		},
	}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)

	finishTx := findMempoolFinish(h.MempoolTxs())
	confirmTx := findMempoolConfirm(h.MempoolTxs())
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{finishTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)

	resp2, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.Nil(t, resp2.CachedResponseBody)
	require.False(t, resp2.LiveAttach)
	require.Equal(t, observability.ReasonInferenceDisappeared, resp2.ReceiptReason)
}

func TestHost_ConfirmStartNotDuplicatedOnReconnect(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 10000))
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[1], stub.NewInferenceEngine(), "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)
	firstSig := append([]byte(nil), resp.Receipt...)

	// Mark executing with a live stream so reconnect hits alreadyExecuting path.
	stream := newSpooledLiveStream(t)
	h.mu.Lock()
	h.executing[1] = struct{}{}
	h.liveStreams[1] = stream
	h.mu.Unlock()

	resp2, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.True(t, resp2.LiveAttach)
	require.True(t, bytes.Equal(firstSig, resp2.Receipt), "reconnect must reuse original receipt")

	confirmCount := 0
	for _, tx := range h.MempoolTxs() {
		if cs := tx.GetConfirmStart(); cs != nil && cs.InferenceId == 1 {
			confirmCount++
		}
	}
	require.Equal(t, 1, confirmCount)

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = stream.Write([]byte("data: hi\n"))
		stream.Close(nil)
	}()
	rec := newFlushRecorder()
	require.NoError(t, h.AttachLiveStream(1, rec, 0, 0))
	require.Contains(t, rec.body(), "data: hi")
}

package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/host"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// poolFullStore never gets a connection for a diff batch.
type poolFullStore struct {
	*storage.Memory
}

func (s *poolFullStore) AppendDiffBatch(ctx context.Context, _ string, _ []types.DiffRecord) error {
	<-ctx.Done()
	return fmt.Errorf("%w: %w", storage.ErrConnWait, ctx.Err())
}

func TestHandleInference_StorageBusyIs503WithHeader(t *testing.T) {
	busy := &poolFullStore{Memory: storage.NewMemory()}
	env := setupServerEnvHost(t, []host.HostOption{host.WithStorage(busy)})
	busy.Memory = env.store

	body := chatTestInferenceJSON(t, env)
	ts := time.Now().Unix()
	sig, err := SignRequest(env.userSigner, "escrow-1", body, ts)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, testRoutePrefix+"/sessions/escrow-1/chat/completions",
		strings.NewReader(string(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, DevshardErrorStorageBusy, rec.Header().Get(HeaderDevshardError))
	require.NotContains(t, rec.Body.String(), "devshard_receipt")
	require.Zero(t, env.server.Host().LatestNonce(), "a busy write applies nothing")

	status := &UpstreamStatusError{StatusCode: rec.Code, DevshardError: rec.Header().Get(HeaderDevshardError)}
	require.True(t, IsStorageBusy(fmt.Errorf("send: %w", status)))
}

func TestIsStorageBusy(t *testing.T) {
	require.True(t, IsStorageBusy(&UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, DevshardError: DevshardErrorStorageBusy}))
	require.False(t, IsStorageBusy(&UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, DevshardError: DevshardErrorRequestsDisabled}))
	require.False(t, IsStorageBusy(&UpstreamStatusError{StatusCode: http.StatusInternalServerError, DevshardError: DevshardErrorStorageBusy}))

	ce := connect.NewError(connect.CodeUnavailable, errors.New("busy"))
	ce.Meta().Set(HeaderDevshardError, DevshardErrorStorageBusy)
	require.True(t, IsStorageBusy(ce))
	require.False(t, IsStorageBusy(connect.NewError(connect.CodeUnavailable, errors.New("draining"))))
	require.False(t, IsStorageBusy(errors.New("storage_busy")))
}

// cancelAwareEngine records whether its context was canceled while it ran.
type cancelAwareEngine struct {
	inner    *stub.InferenceEngine
	started  chan struct{}
	release  chan struct{}
	canceled atomic.Bool
}

func (e *cancelAwareEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	close(e.started)
	<-e.release
	if ctx.Err() != nil {
		e.canceled.Store(true)
	}
	return e.inner.Execute(ctx, req)
}

// Test flow:
//  1. The client receives the receipt, then disconnects while the engine runs.
//  2. The engine's context is not canceled with the request.
//  3. The Finish is queued.
func TestServeInference_ClientCancelAfterReceiptStillFinishes(t *testing.T) {
	engine := &cancelAwareEngine{inner: stub.NewInferenceEngine(), started: make(chan struct{}), release: make(chan struct{})}
	env := setupServerEnvEngine(t, engine, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-engine.started
		cancel()
		close(engine.release)
	}()
	var frames atomic.Int32
	sink := NewChatFrameSink(func([]byte) error {
		if frames.Add(1) > 1 && ctx.Err() != nil {
			return errors.New("client gone")
		}
		return nil
	})
	_ = env.server.ServeInference(ctx, InferenceCall{
		SessionID: "escrow-1",
		Sender:    env.userSigner.Address(),
		Body:      chatTestInferenceJSON(t, env),
		Source:    "test",
		Sink:      sink,
	})

	require.False(t, engine.canceled.Load(), "the execution outlives the request")
	require.Eventually(t, func() bool { return mempoolHasFinish(env.server.Host().MempoolTxs()) },
		5*time.Second, 5*time.Millisecond)
}

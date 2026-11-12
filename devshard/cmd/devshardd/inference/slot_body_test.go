package inference

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"
	devshardpkg "devshard"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type slotTestBody struct {
	read   func([]byte) (int, error)
	closed atomic.Int32
	err    error
}

func (b *slotTestBody) Read(p []byte) (int, error) { return b.read(p) }

func (b *slotTestBody) Close() error {
	b.closed.Add(1)
	return b.err
}

func TestSlotBodyTerminalReads(t *testing.T) {
	readFailure := errors.New("upstream failed")
	closeFailure := errors.New("close failed")
	for _, tc := range []struct {
		name    string
		err     error
		outcome nmgen.ReleaseOutcome
		closed  int32
	}{
		{"EOF", io.EOF, nmgen.ReleaseOutcome_SUCCESS, 0},
		{"read failure", readFailure, nmgen.ReleaseOutcome_TRANSPORT_ERROR, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &slotTestBody{err: closeFailure, read: func(p []byte) (int, error) {
				return copy(p, "last"), tc.err
			}}
			var releases atomic.Int32
			type releaseEvent struct {
				outcome nmgen.ReleaseOutcome
				closed  int32
			}
			released := make(chan releaseEvent, 2)
			body := holdSlot(context.Background(), raw, func(outcome nmgen.ReleaseOutcome) {
				releases.Add(1)
				released <- releaseEvent{outcome, raw.closed.Load()}
			})
			p := make([]byte, 8)
			n, err := body.Read(p)
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, "last", string(p[:n]))
			select {
			case event := <-released:
				require.Equal(t, tc.outcome, event.outcome)
				require.Equal(t, tc.closed, event.closed)
			case <-time.After(time.Second):
				t.Fatal("terminal read did not release the slot")
			}
			require.ErrorIs(t, body.Close(), closeFailure)
			require.ErrorIs(t, body.Close(), closeFailure)
			require.Equal(t, int32(1), raw.closed.Load())
			require.Equal(t, int32(1), releases.Load())
		})
	}
}

func TestSlotBodyCancellationWithoutReader(t *testing.T) {
	for _, alreadyCanceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "after handoff", true: "before handoff"}[alreadyCanceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if alreadyCanceled {
				cancel()
			}
			raw := &slotTestBody{read: func([]byte) (int, error) { return 0, io.EOF }}
			released := make(chan nmgen.ReleaseOutcome, 2)
			body := holdSlot(ctx, raw, func(outcome nmgen.ReleaseOutcome) { released <- outcome })
			cancel()
			select {
			case outcome := <-released:
				require.Equal(t, nmgen.ReleaseOutcome_SUCCESS, outcome)
				require.Equal(t, int32(1), raw.closed.Load())
			case <-time.After(time.Second):
				t.Fatal("execution cancellation did not release the unread body")
			}
			require.NoError(t, body.Close())
			require.Equal(t, int32(1), raw.closed.Load())
			require.Empty(t, released)
		})
	}
}

type blockedSlotWriter struct {
	http.ResponseWriter
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (w *blockedSlotWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.resume
	return w.ResponseWriter.Write(p)
}

func slotTestEngine(t *testing.T, mode string, endpoint string, release func(context.Context, *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error), acquire func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error)) (*Engine, *mlnodeclient.Cache) {
	t.Helper()
	if mode != "grpc" {
		acquire = func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "dapi unavailable")
		}
	}
	ml := startEngineMLClient(t, &engineMockNM{acquireFunc: acquire, releaseFunc: release})
	capacity := mlnodeclient.NewCache(nil, mlnodeclient.CacheOptions{UnknownMaxConcurrent: 4})
	node := "node-1"
	if mode == "unknown fallback" {
		node = "other-node"
	}
	capacity.ApplyPollForTest([]*nmgen.NodeCapacityEntry{{NodeId: node, Model: "model-a", MaxConcurrent: 4}})
	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("model-a", "node-1", endpoint)
	return newTestEngine(ml, mgr, capacity), capacity
}

func TestSlotBodyCancellationWhileProxyWriteBlocked(t *testing.T) {
	for _, mode := range []string{"grpc", "known fallback", "unknown fallback"} {
		t.Run(mode, func(t *testing.T) {
			mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: first\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			t.Cleanup(mlSrv.Close)
			var releases atomic.Int32
			released := make(chan nmgen.ReleaseOutcome, 2)
			eng, capacity := slotTestEngine(t, mode, mlSrv.URL,
				func(ctx context.Context, req *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					releases.Add(1)
					released <- req.Outcome
					return &nmgen.ReleaseMLNodeResponse{}, nil
				},
				func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
					return &nmgen.AcquireMLNodeResponse{LockId: "lock-1", NodeId: "node-1", Endpoint: mlSrv.URL}, nil
				})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			resp, err := eng.executeMLRequest(ctx, "model-a", "42", []byte(`{"stream":true}`))
			require.NoError(t, err)
			writer := &blockedSlotWriter{ResponseWriter: httptest.NewRecorder(), entered: make(chan struct{}), resume: make(chan struct{})}
			done := make(chan error, 1)
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(writer.resume) }) }
			t.Cleanup(func() {
				cancel()
				resume()
				_ = resp.Body.Close()
			})
			go func() { done <- proxyTextStreamResponse(resp, writer, nil, "slot-test") }()
			select {
			case <-writer.entered:
			case <-time.After(time.Second):
				t.Fatal("proxy did not reach the blocked write")
			}
			cancel()
			if mode == "grpc" {
				select {
				case outcome := <-released:
					require.Equal(t, nmgen.ReleaseOutcome_SUCCESS, outcome)
				case <-time.After(time.Second):
					t.Fatal("cancellation did not release while the downstream write was blocked")
				}
			} else {
				acquire := func() bool { return capacity.TryAcquire("node-1", "model-a") }
				release := func() { capacity.Release("node-1", "model-a") }
				if mode == "unknown fallback" {
					acquire = func() bool { return capacity.TryAcquireUnknown("node-1", "model-a") }
					release = func() { capacity.ReleaseUnknown("node-1", "model-a") }
				}
				require.Eventually(t, acquire, time.Second, time.Millisecond)
				release()
			}
			resume()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("proxy did not stop after unblocking its writer")
			}
			require.NoError(t, resp.Body.Close())
			if mode == "grpc" {
				require.Equal(t, int32(1), releases.Load())
			}
		})
	}
}

func TestSlotBodyEOFAdmitsNextRequestBeforeClose(t *testing.T) {
	for _, mode := range []string{"grpc", "known fallback", "unknown fallback"} {
		t.Run(mode, func(t *testing.T) {
			finish := make(chan struct{})
			var finishOnce sync.Once
			finishBody := func() { finishOnce.Do(func() { close(finish) }) }
			var hits, active, maxActive atomic.Int32
			mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cur := active.Add(1)
				defer active.Add(-1)
				for prev := maxActive.Load(); cur > prev; prev = maxActive.Load() {
					if maxActive.CompareAndSwap(prev, cur) {
						break
					}
				}
				n := hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: first\n\n")
				w.(http.Flusher).Flush()
				if n == 1 {
					select {
					case <-finish:
					case <-r.Context().Done():
					}
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			t.Cleanup(func() { finishBody(); mlSrv.Close() })
			var locked atomic.Bool
			eng, _ := slotTestEngine(t, mode, mlSrv.URL,
				func(context.Context, *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
					locked.Store(false)
					return &nmgen.ReleaseMLNodeResponse{}, nil
				},
				func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
					if !locked.CompareAndSwap(false, true) {
						return nil, status.Error(codes.ResourceExhausted, "slot occupied")
					}
					return &nmgen.AcquireMLNodeResponse{LockId: "lock-1", NodeId: "node-1", Endpoint: mlSrv.URL}, nil
				})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			t.Cleanup(cancel)
			first, err := eng.executeMLRequest(ctx, "model-a", "42", []byte(`{"stream":true}`))
			require.NoError(t, err)
			t.Cleanup(func() { _ = first.Body.Close() })
			second := make(chan error, 1)
			go func() {
				resp, err := eng.executeMLRequest(ctx, "model-a", "42", []byte(`{"stream":true}`))
				if err == nil {
					_, err = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				second <- err
			}()
			select {
			case err := <-second:
				t.Fatalf("second request completed while the first body was open: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			finishBody()
			_, err = io.Copy(io.Discard, first.Body)
			require.NoError(t, err)
			select {
			case err := <-second:
				require.NoError(t, err)
			case <-time.After(4 * time.Second):
				t.Fatal("EOF did not admit the second request before the first body was closed")
			}
			require.Equal(t, int32(2), hits.Load())
			require.Equal(t, int32(1), maxActive.Load())
		})
	}
}

func TestSlotBodyCancellationDoesNotFollowParentDisconnect(t *testing.T) {
	parent, disconnect := context.WithCancel(context.Background())
	execution, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	var releases atomic.Int32
	body := holdSlot(execution, io.NopCloser(strings.NewReader("answer")), func(nmgen.ReleaseOutcome) { releases.Add(1) })
	disconnect()
	p := make([]byte, 3)
	_, err := body.Read(p)
	require.NoError(t, err)
	require.Zero(t, releases.Load())
	require.NoError(t, body.Close())
	require.Equal(t, int32(1), releases.Load())
}

func TestSlotBodyConcurrentTerminalEvents(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		raw := &slotTestBody{read: func(p []byte) (int, error) { return copy(p, "last"), io.EOF }}
		var releases atomic.Int32
		body := holdSlot(ctx, raw, func(nmgen.ReleaseOutcome) { releases.Add(1) })
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, call := range []func(){
			func() { _, _ = body.Read(make([]byte, 8)) },
			func() { _ = body.Close() },
			func() { _ = body.Close() },
			cancel,
		} {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; call() }()
		}
		close(start)
		wg.Wait()
		require.Equal(t, int32(1), releases.Load())
		require.Equal(t, int32(1), raw.closed.Load())
	}
}

func TestSlotBodyEmptyResponses(t *testing.T) {
	for _, raw := range []io.ReadCloser{nil, http.NoBody} {
		var releases int
		body := holdSlot(context.Background(), raw, func(outcome nmgen.ReleaseOutcome) {
			releases++
			require.Equal(t, nmgen.ReleaseOutcome_SUCCESS, outcome)
		})
		require.Equal(t, raw, body)
		require.Equal(t, 1, releases)
	}
}

type slotPayloadStore struct {
	recordingPayloadStore
	entered chan struct{}
	resume  <-chan struct{}
}

func (s *slotPayloadStore) Store(ctx context.Context, escrowID string, inferenceID, epochID uint64, prompt, response []byte) error {
	close(s.entered)
	select {
	case <-s.resume:
		return s.recordingPayloadStore.Store(ctx, escrowID, inferenceID, epochID, prompt, response)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSlotBodyEOFReleasesDuringPayloadStorage(t *testing.T) {
	response := `{"id":"x","object":"chat.completion","created":1,"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`
	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(mlSrv.Close)
	releaseStarted := make(chan struct{})
	allowRelease := make(chan struct{})
	allowStore := make(chan struct{})
	var releaseOnce, storeOnce sync.Once
	unblockRelease := func() { releaseOnce.Do(func() { close(allowRelease) }) }
	unblockStore := func() { storeOnce.Do(func() { close(allowStore) }) }
	t.Cleanup(unblockRelease)
	t.Cleanup(unblockStore)
	eng, _ := slotTestEngine(t, "grpc", mlSrv.URL,
		func(ctx context.Context, _ *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			close(releaseStarted)
			select {
			case <-allowRelease:
				return &nmgen.ReleaseMLNodeResponse{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock-1", NodeId: "node-1", Endpoint: mlSrv.URL}, nil
		})
	store := &slotPayloadStore{entered: make(chan struct{}), resume: allowStore}
	type execution struct {
		result *devshardpkg.ExecuteResult
		err    error
	}
	done := make(chan execution, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	go func() {
		result, err := executeInference(ctx, devshardpkg.ExecuteRequest{
			InferenceID: 1, EscrowID: "42", Model: "model-a",
			Prompt: []byte(`{"model":"model-a","messages":[{"role":"user","content":"hi"}]}`),
		}, store, 1, func(ctx context.Context, model string, body []byte) (*http.Response, error) {
			return eng.executeMLRequest(ctx, model, "42", body)
		}, fixedChainParams{}, true, fixedVocabularySize(0))
		done <- execution{result, err}
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("release RPC blocked payload storage after EOF")
	}
	select {
	case <-releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("payload storage still owns the completed ML reservation")
	}
	unblockRelease()
	unblockStore()
	select {
	case execution := <-done:
		require.NoError(t, execution.err)
		require.Equal(t, uint64(7), execution.result.InputTokens)
		require.Equal(t, uint64(1), execution.result.OutputTokens)
		hash := sha256.Sum256(store.responsePayload)
		require.Equal(t, hash[:], execution.result.ResponseHash)
	case <-time.After(time.Second):
		t.Fatal("execution did not complete after releasing storage")
	}
}

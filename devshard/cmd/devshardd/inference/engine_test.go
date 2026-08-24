package inference

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"
	"devshard/observability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type engineMockNM struct {
	nmgen.UnimplementedNodeManagerServer
	acquireFunc func(ctx context.Context, req *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error)
	releaseFunc func(ctx context.Context, req *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error)
}

func (m *engineMockNM) AcquireMLNode(ctx context.Context, req *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
	return m.acquireFunc(ctx, req)
}

func (m *engineMockNM) ReleaseMLNode(ctx context.Context, req *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
	if m.releaseFunc != nil {
		return m.releaseFunc(ctx, req)
	}
	return &nmgen.ReleaseMLNodeResponse{}, nil
}

func startEngineMLClient(t *testing.T, srv *engineMockNM) *mlnodeclient.Client {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcSrv := grpc.NewServer()
	nmgen.RegisterNodeManagerServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return mlnodeclient.ClientForTest(nmgen.NewNodeManagerClient(conn))
}

func newTestEngine(ml *mlnodeclient.Client, mgr *mlnodeclient.Manager, capacity *mlnodeclient.Cache) *Engine {
	return &Engine{
		mlClient:   ml,
		mgr:        mgr,
		capacity:   capacity,
		httpClient: http.DefaultClient,
	}
}

func TestDoWithLockedNode_GRPCSuccessObserves(t *testing.T) {
	var releases atomic.Int32
	mlHits := atomic.Int32{}
	var gotEscrowID string

	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mlHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, req *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			assert.Equal(t, "model-a", req.Model)
			gotEscrowID = req.EscrowId
			return &nmgen.AcquireMLNodeResponse{
				LockId:   "lock-1",
				Endpoint: mlSrv.URL,
				NodeId:   "node-1",
			}, nil
		},
		releaseFunc: func(_ context.Context, req *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			releases.Add(1)
			assert.Equal(t, "lock-1", req.LockId)
			assert.Equal(t, nmgen.ReleaseOutcome_SUCCESS, req.Outcome)
			return &nmgen.ReleaseMLNodeResponse{}, nil
		},
	})

	mgr := mlnodeclient.NewManager(time.Hour)
	eng := newTestEngine(ml, mgr, nil)

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "42",
		func(endpoint string) (*http.Response, error) {
			return http.Get(endpoint)
		})
	require.NoError(t, err)
	require.NotNil(t, resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.Equal(t, "42", gotEscrowID)
	assert.Equal(t, int32(1), mlHits.Load())
	assert.Equal(t, int32(1), releases.Load())

	// Passive observe: node is in the cache for fallback.
	endpoint, nodeID, ok := mgr.PickNode("model-a", nil)
	require.True(t, ok)
	assert.Equal(t, "node-1", nodeID)
	assert.Equal(t, mlSrv.URL, endpoint)
}

// recordEngineSpans installs a recording tracer provider for the duration of a test.
func recordEngineSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

func engineSpanByName(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func engineSpanAttr(span sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

// TestDoWithLockedNode_EmitsMLNodeSpans covers T5a: the dapi hop and the ML
// HTTP call hang off the caller's trace, so a request's node selection is
// visible in Tempo rather than only in metrics.
func TestDoWithLockedNode_EmitsMLNodeSpans(t *testing.T) {
	recorder := recordEngineSpans(t)

	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock-1", Endpoint: mlSrv.URL, NodeId: "node-1"}, nil
		},
	})
	eng := newTestEngine(ml, mlnodeclient.NewManager(time.Hour), nil)

	ctx, caller := otel.Tracer("test").Start(context.Background(), "caller")
	resp, err := eng.doWithLockedNode(ctx, observability.PathExecute, "model-a", "42",
		func(endpoint string) (*http.Response, error) { return http.Get(endpoint) })
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	caller.End()

	spans := recorder.Ended()
	acquire := engineSpanByName(spans, "devshardd.mlnode.acquire")
	require.NotNil(t, acquire, "missing acquire span")
	assert.Equal(t, caller.SpanContext().TraceID(), acquire.SpanContext().TraceID())
	assert.Equal(t, "node-1", engineSpanAttr(acquire, "mlnode.node.id"))
	assert.Equal(t, mlSrv.URL, engineSpanAttr(acquire, "mlnode.endpoint"))
	assert.Equal(t, "lock-1", engineSpanAttr(acquire, "mlnode.lock_id"))
	assert.Equal(t, "model-a", engineSpanAttr(acquire, "model"))

	release := engineSpanByName(spans, "devshardd.mlnode.release")
	require.NotNil(t, release, "missing release span")
	assert.Equal(t, caller.SpanContext().TraceID(), release.SpanContext().TraceID())
	assert.Equal(t, "SUCCESS", engineSpanAttr(release, "mlnode.release_outcome"))
	assert.Equal(t, "lock-1", engineSpanAttr(release, "mlnode.lock_id"))
}

// TestDoWithLockedNode_AcquireFailureMarksSpan keeps a failed acquire visible
// in the trace instead of leaving a silently OK span.
func TestDoWithLockedNode_AcquireFailureMarksSpan(t *testing.T) {
	recorder := recordEngineSpans(t)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.ResourceExhausted, "no nodes available")
		},
	})
	eng := newTestEngine(ml, mlnodeclient.NewManager(time.Hour), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := eng.doWithLockedNode(ctx, observability.PathExecute, "model-a", "",
		func(endpoint string) (*http.Response, error) { return http.Get(endpoint) })
	require.Error(t, err)

	acquire := engineSpanByName(recorder.Ended(), "devshardd.mlnode.acquire")
	require.NotNil(t, acquire, "a failed acquire must still leave a span")
	assert.Equal(t, otelcodes.Error, acquire.Status().Code)
	assert.Nil(t, engineSpanByName(recorder.Ended(), "devshardd.mlnode.release"),
		"nothing was locked, so nothing may be released")
}

func TestDoWithLockedNode_UnavailableFallsBack(t *testing.T) {
	var acquires, releases atomic.Int32
	mlHits := atomic.Int32{}

	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mlHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			acquires.Add(1)
			return nil, status.Error(codes.Unavailable, "dapi down")
		},
		releaseFunc: func(_ context.Context, _ *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			releases.Add(1)
			return &nmgen.ReleaseMLNodeResponse{}, nil
		},
	})

	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("model-a", "node-1", mlSrv.URL)
	eng := newTestEngine(ml, mgr, nil)

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "",
		func(endpoint string) (*http.Response, error) {
			return http.Get(endpoint)
		})
	require.NoError(t, err)
	require.NotNil(t, resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.Equal(t, int32(1), acquires.Load())
	assert.Equal(t, int32(0), releases.Load(), "fallback must not Release")
	assert.Equal(t, int32(1), mlHits.Load())
}

func TestDoWithLockedNode_ResourceExhaustedDoesNotFallback(t *testing.T) {
	var acquires atomic.Int32
	mlHits := atomic.Int32{}

	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mlHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			acquires.Add(1)
			return nil, status.Error(codes.ResourceExhausted, "no nodes available")
		},
	})

	mgr := mlnodeclient.NewManager(time.Hour)
	// Cache has a node — fallback must not use it on ResourceExhausted.
	mgr.Observe("model-a", "node-1", mlSrv.URL)
	eng := newTestEngine(ml, mgr, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := eng.doWithLockedNode(ctx, observability.PathExecute, "model-a", "",
		func(endpoint string) (*http.Response, error) {
			return http.Get(endpoint)
		})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, int32(0), mlHits.Load(), "must not fall back to cached node")
	assert.GreaterOrEqual(t, acquires.Load(), int32(1))
}

func TestDoWithLockedNode_FallbackRotatesOn5xx(t *testing.T) {
	var hits1, hits2 atomic.Int32

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits1.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits2.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(good.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "dapi down")
		},
	})

	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("model-a", "node-bad", bad.URL)
	mgr.Observe("model-a", "node-good", good.URL)
	eng := newTestEngine(ml, mgr, nil)

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "",
		func(endpoint string) (*http.Response, error) {
			return http.Get(endpoint)
		})
	require.NoError(t, err)
	require.NotNil(t, resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.Equal(t, int32(1), hits1.Load())
	assert.Equal(t, int32(1), hits2.Load())
}

func TestDoWithLockedNode_FallbackEmptyCacheFails(t *testing.T) {
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "dapi down")
		},
	})

	mgr := mlnodeclient.NewManager(time.Hour)
	eng := newTestEngine(ml, mgr, nil)

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "",
		func(endpoint string) (*http.Response, error) {
			return http.Get(endpoint)
		})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "no cached nodes")
}

func TestShouldFallback(t *testing.T) {
	assert.True(t, shouldFallback(mlnodeclient.ErrUnavailable))
	assert.True(t, shouldFallback(status.Error(codes.Unavailable, "x")))
	assert.True(t, shouldFallback(status.Error(codes.DeadlineExceeded, "timeout")))
	assert.False(t, shouldFallback(mlnodeclient.ErrNoNodesAvailable))
	assert.False(t, shouldFallback(status.Error(codes.ResourceExhausted, "x")))
	assert.False(t, shouldFallback(status.Error(codes.Internal, "x")))
}

func TestFallback_RespectsLocalInFlight(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "dapi down")
		},
	})

	now := time.Unix(1_700_000_000, 0)
	capacity := mlnodeclient.NewCache(nil, mlnodeclient.CacheOptions{
		Now: func() time.Time { return now },
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return map[uint64]float64{}, now // fresh → floor divisor 4
		},
	})
	// MaxConcurrent=4, divisor=4 → EffectiveMax=1; LockCount=0 → 1 local slot.
	capacity.ApplyPollForTest([]*nmgen.NodeCapacityEntry{
		{NodeId: "node-1", Model: "model-a", MaxConcurrent: 4, LockCount: 0},
	})
	require.True(t, capacity.HasObservedCapacity())
	require.Equal(t, 1, capacity.EffectiveMax("node-1"))

	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("model-a", "node-1", mlSrv.URL)
	eng := newTestEngine(ml, mgr, capacity)

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "",
				func(endpoint string) (*http.Response, error) {
					return http.Get(endpoint)
				})
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), maxInFlight.Load(), "fallback in-flight must be capped at EffectiveMax")
}

func TestFallback_NoCapacityUnbounded(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		once.Do(func() { close(started) })
		<-release
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "dapi down")
		},
	})

	// Old DAPI: capacity never observed → unbounded fallback.
	capacity := mlnodeclient.NewCache(nil, mlnodeclient.CacheOptions{})
	capacity.SetUnsupportedForTest()
	require.False(t, capacity.HasObservedCapacity())

	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("model-a", "node-1", mlSrv.URL)
	eng := newTestEngine(ml, mgr, capacity)

	const workers = 6
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "",
				func(endpoint string) (*http.Response, error) {
					return http.Get(endpoint)
				})
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not reach ML server")
	}
	require.Eventually(t, func() bool {
		return maxInFlight.Load() >= 2
	}, time.Second, 5*time.Millisecond, "without capacity bound, concurrent fallback must proceed")
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	assert.GreaterOrEqual(t, maxInFlight.Load(), int32(2))
}

func TestFallback_UnknownNodeBounded(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	mlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer inFlight.Add(-1)
		time.Sleep(40 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mlSrv.Close)

	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "dapi down")
		},
	})

	now := time.Unix(1_700_000_000, 0)
	capacity := mlnodeclient.NewCache(nil, mlnodeclient.CacheOptions{
		Now: func() time.Time { return now },
		ActiveLoad: func() (map[uint64]float64, time.Time) {
			return map[uint64]float64{}, now // fresh → floor divisor 4
		},
		UnknownMaxConcurrent: 4, // 4/4 → 1 synthetic slot
	})
	// Capacity has been observed (for some other node) so limit==true, but the
	// node PickNode serves was never reported → capacity-unknown.
	capacity.ApplyPollForTest([]*nmgen.NodeCapacityEntry{
		{NodeId: "other", Model: "model-a", MaxConcurrent: 40, LockCount: 0},
	})
	require.True(t, capacity.HasObservedCapacity())
	_, known := capacity.Get("node-ghost")
	require.False(t, known)

	mgr := mlnodeclient.NewManager(time.Hour)
	mgr.Observe("model-a", "node-ghost", mlSrv.URL)
	eng := newTestEngine(ml, mgr, capacity)

	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "",
				func(endpoint string) (*http.Response, error) {
					return http.Get(endpoint)
				})
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), maxInFlight.Load(), "capacity-unknown node must be bounded, not unbounded")
}

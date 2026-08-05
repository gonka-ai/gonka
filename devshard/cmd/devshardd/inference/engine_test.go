package inference

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mlnodeclient "common/nodemanager"
	nmgen "common/nodemanager/gen"
	"devshard/observability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// captureBodyMLServer starts a fake ML node that records the last request
// body it received, so tests can inspect what executeMLRequest actually sent.
func captureBodyMLServer(t *testing.T, gotBody *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestEngineForNode(t *testing.T, endpoint string) *Engine {
	t.Helper()
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, _ *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock-1", Endpoint: endpoint, NodeId: "node-1"}, nil
		},
		releaseFunc: func(_ context.Context, _ *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			return &nmgen.ReleaseMLNodeResponse{}, nil
		},
	})
	return newTestEngine(ml, nil, nil)
}

// newTestEngineCapturingSessionID wires an Engine to a fake NodeManager that
// records the session id carried by every Acquire RPC, so tests can prove
// whether the id itself -- not just its two known effects -- left the engine.
func newTestEngineCapturingSessionID(t *testing.T, endpoint string, gotSessionID *string) *Engine {
	t.Helper()
	ml := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(_ context.Context, req *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			*gotSessionID = req.GetSessionId()
			return &nmgen.AcquireMLNodeResponse{LockId: "lock-1", Endpoint: endpoint, NodeId: "node-1"}, nil
		},
		releaseFunc: func(_ context.Context, _ *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			return &nmgen.ReleaseMLNodeResponse{}, nil
		},
	})
	return newTestEngine(ml, nil, nil)
}

func TestExecuteMLRequest_AffinityDisabledNeverSendsSessionIDToAcquire(t *testing.T) {
	var gotBody []byte
	srv := captureBodyMLServer(t, &gotBody)
	var gotSessionID string
	eng := newTestEngineCapturingSessionID(t, srv.URL, &gotSessionID) // affinityEnabled defaults to false

	resp, err := eng.executeMLRequest(context.Background(), "model-a", "escrow-1", "sess-A", []byte(`{"model":"m","messages":[]}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.Empty(t, gotSessionID, "the session id must never reach the Acquire RPC while the participant's affinity switch is off")
}

func TestExecuteMLRequest_AffinityEnabledSendsSessionIDToAcquire(t *testing.T) {
	var gotBody []byte
	srv := captureBodyMLServer(t, &gotBody)
	var gotSessionID string
	eng := newTestEngineCapturingSessionID(t, srv.URL, &gotSessionID)
	eng.affinityEnabled = true

	resp, err := eng.executeMLRequest(context.Background(), "model-a", "escrow-1", "sess-A", []byte(`{"model":"m","messages":[]}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	assert.Equal(t, "sess-A", gotSessionID, "the session id must reach Acquire once the participant enables affinity")
}

// The gateway enabling stickiness and the participant enabling it are separate operator
// decisions, so a stock participant must still isolate the caches of the clients it serves.
func TestExecuteMLRequest_AffinityDisabledStillSaltsBody(t *testing.T) {
	var gotBody []byte
	srv := captureBodyMLServer(t, &gotBody)
	eng := newTestEngineForNode(t, srv.URL) // affinityEnabled defaults to false

	resp, err := eng.executeMLRequest(context.Background(), "model-a", "escrow-1", "sess-A", []byte(`{"model":"m","messages":[]}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	var sent map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	_, hasSalt := sent["cache_salt"]
	assert.True(t, hasSalt, "a session id must salt the cache namespace whatever the participant's stickiness switch says")
}

func TestExecuteMLRequest_SaltsBody(t *testing.T) {
	var gotBody []byte
	srv := captureBodyMLServer(t, &gotBody)
	eng := newTestEngineForNode(t, srv.URL)
	eng.affinityEnabled = true

	resp, err := eng.executeMLRequest(context.Background(), "model-a", "escrow-1", "sess-A", []byte(`{"model":"m","messages":[]}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	var sent map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	salt, hasSalt := sent["cache_salt"]
	assert.True(t, hasSalt, "a session id must carry a cache_salt to the ml node")
	assert.NotEmpty(t, salt)
}

func TestExecuteMLRequest_OversizedSessionIDIsDropped(t *testing.T) {
	var gotBody []byte
	srv := captureBodyMLServer(t, &gotBody)
	var gotSessionID string
	eng := newTestEngineCapturingSessionID(t, srv.URL, &gotSessionID)
	eng.affinityEnabled = true
	oversized := strings.Repeat("k", maxSessionIDLength+1)

	resp, err := eng.executeMLRequest(context.Background(), "model-a", "escrow-1", oversized, []byte(`{"model":"m","messages":[]}`))
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	var sent map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	_, hasSalt := sent["cache_salt"]
	assert.False(t, hasSalt, "a session id past the bound must not key a cache namespace")
	assert.Empty(t, gotSessionID, "a session id past the bound must not key an mlnode binding either")
}

func TestExecuteMLRequest_SaltIsScopedToEscrow(t *testing.T) {
	saltFor := func(escrowID string) string {
		var gotBody []byte
		srv := captureBodyMLServer(t, &gotBody)
		eng := newTestEngineForNode(t, srv.URL)
		eng.affinityEnabled = true

		resp, err := eng.executeMLRequest(context.Background(), "model-a", escrowID, "sess-A", []byte(`{"model":"m","messages":[]}`))
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		var sent map[string]any
		require.NoError(t, json.Unmarshal(gotBody, &sent))
		salt, _ := sent["cache_salt"].(string)
		require.NotEmpty(t, salt)
		return salt
	}

	assert.NotEqual(t, saltFor("escrow-1"), saltFor("escrow-2"),
		"the same session id served under two escrows must reach two cache namespaces")
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

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "42", "",
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

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "", "",
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

	resp, err := eng.doWithLockedNode(ctx, observability.PathExecute, "model-a", "", "",
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

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "", "",
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

	resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "", "",
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
			resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "", "",
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
			resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "", "",
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
			resp, err := eng.doWithLockedNode(context.Background(), observability.PathExecute, "model-a", "", "",
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


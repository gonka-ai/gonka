package pocstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"decentralized-api/pocstream/gen"

	"github.com/labstack/echo/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type fakeStream struct {
	grpc.ClientStream
	ctx       context.Context
	callbacks chan *gen.Callback
}

func (s *fakeStream) Recv() (*gen.Callback, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case cb, ok := <-s.callbacks:
		if !ok {
			return nil, io.EOF
		}
		return cb, nil
	}
}

func (s *fakeStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeStream) Trailer() metadata.MD         { return nil }
func (s *fakeStream) CloseSend() error             { return nil }
func (s *fakeStream) Context() context.Context     { return s.ctx }

type fakeClient struct {
	mu          sync.Mutex
	pending     []*gen.Callback
	acked       []string
	resumeSeen  []string
	streamCount int
}

func (c *fakeClient) push(cb *gen.Callback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(c.pending, cb)
}

func (c *fakeClient) ackedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.acked...)
}

func (c *fakeClient) StreamCallbacks(ctx context.Context, in *gen.StreamCallbacksRequest, opts ...grpc.CallOption) (gen.PoCCallbackStream_StreamCallbacksClient, error) {
	c.mu.Lock()
	c.streamCount++
	c.resumeSeen = append(c.resumeSeen, in.ResumeAfterId)
	acked := make(map[string]bool, len(c.acked))
	for _, id := range c.acked {
		acked[id] = true
	}
	stream := &fakeStream{ctx: ctx, callbacks: make(chan *gen.Callback, 64)}
	for _, cb := range c.pending {
		if !acked[cb.Id] {
			stream.callbacks <- cb
		}
	}
	close(stream.callbacks)
	c.mu.Unlock()
	return stream, nil
}

func (c *fakeClient) AckCallbacks(ctx context.Context, in *gen.AckCallbacksRequest, opts ...grpc.CallOption) (*gen.AckCallbacksResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acked = append(c.acked, in.Ids...)
	return &gen.AckCallbacksResponse{}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

type fakeGate struct{ active bool }

func (g *fakeGate) StreamActive() bool { return g.active }

func newTestManager(handler http.Handler, gate PhaseGate, client *fakeClient) *Manager {
	targets := func() ([]StreamTarget, error) {
		return []StreamTarget{{NodeID: "node1", Address: "fake:8090"}}, nil
	}
	return newTestManagerWithTargets(handler, gate, client, targets)
}

func newTestManagerWithTargets(handler http.Handler, gate PhaseGate, client *fakeClient, targets TargetSource) *Manager {
	return NewManager(handler, gate, targets,
		WithDialer(func(address string) (gen.PoCCallbackStreamClient, io.Closer, error) {
			return client, nopCloser{}, nil
		}),
		WithConfig(Config{
			InitialBackoff: 5 * time.Millisecond,
			MaxBackoff:     20 * time.Millisecond,
			SyncInterval:   5 * time.Millisecond,
		}),
	)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestManagerDispatchesSamePathAsHTTP(t *testing.T) {
	var mu sync.Mutex
	var gotModel string
	var gotBody []byte

	e := echo.New()
	e.POST("/v2/poc-batches/:model_id/generated", func(c echo.Context) error {
		decoded, err := url.PathUnescape(c.Param("model_id"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad model id")
		}
		body, _ := io.ReadAll(c.Request().Body)
		mu.Lock()
		gotModel = decoded
		gotBody = body
		mu.Unlock()
		return c.NoContent(http.StatusOK)
	})

	client := &fakeClient{}
	client.push(&gen.Callback{
		Id:   "boot-1",
		Path: "/v2/poc-batches/org%2Fmodel/generated",
		Body: []byte(`{"nonces":[1,2,3]}`),
	})

	manager := newTestManager(e, &fakeGate{active: true}, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool { return len(client.ackedIDs()) == 1 })

	mu.Lock()
	defer mu.Unlock()
	if gotModel != "org/model" {
		t.Errorf("model id = %q, want %q", gotModel, "org/model")
	}
	if string(gotBody) != `{"nonces":[1,2,3]}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestManagerAcksPermanentlyRejectedCallbacks(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	client := &fakeClient{}
	client.push(&gen.Callback{Id: "boot-1", Path: "/v2/poc-batches/m/generated", Body: []byte("{}")})

	manager := newTestManager(handler, &fakeGate{active: true}, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool { return len(client.ackedIDs()) == 1 })
}

func TestManagerRetriesTransientFailures(t *testing.T) {
	var mu sync.Mutex
	failures := 2
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if failures > 0 {
			failures--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	client := &fakeClient{}
	client.push(&gen.Callback{Id: "boot-1", Path: "/v2/poc-batches/m/generated", Body: []byte("{}")})

	manager := newTestManager(handler, &fakeGate{active: true}, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, 2*time.Second, func() bool { return len(client.ackedIDs()) == 1 })

	client.mu.Lock()
	streams := client.streamCount
	client.mu.Unlock()
	if streams < 3 {
		t.Errorf("expected at least 3 stream attempts (2 failures + success), got %d", streams)
	}
}

func TestManagerResumesAfterLastAck(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	client := &fakeClient{}
	client.push(&gen.Callback{Id: "boot-1", Path: "/v2/poc-batches/m/generated", Body: []byte("{}")})

	manager := newTestManager(handler, &fakeGate{active: true}, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool { return len(client.ackedIDs()) == 1 })

	client.push(&gen.Callback{Id: "boot-2", Path: "/v2/poc-batches/m/generated", Body: []byte("{}")})
	waitFor(t, 2*time.Second, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		for _, resume := range client.resumeSeen {
			if resume == "boot-1" || resume == "boot-2" {
				return true
			}
		}
		return false
	})
}

func TestManagerStopsStreamsWhenPhaseEnds(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	gate := &fakeGate{active: true}
	client := &fakeClient{}
	manager := newTestManager(handler, gate, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.streams) == 1
	})

	gate.active = false
	waitFor(t, time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.streams) == 0
	})
}

func TestSyncIsIdempotent(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	client := &fakeClient{}
	manager := newTestManager(handler, &fakeGate{active: true}, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.streams) == 1
	})

	time.Sleep(50 * time.Millisecond)
	manager.mu.Lock()
	count := len(manager.streams)
	manager.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 stream, got %d", count)
	}
}

func TestTargetErrorKeepsStreams(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	client := &fakeClient{}

	var mu sync.Mutex
	fail := false
	targets := func() ([]StreamTarget, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, errors.New("broker unavailable")
		}
		return []StreamTarget{{NodeID: "node1", Address: "fake:8090"}}, nil
	}

	manager := newTestManagerWithTargets(handler, &fakeGate{active: true}, client, targets)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.streams) == 1
	})

	mu.Lock()
	fail = true
	mu.Unlock()

	time.Sleep(50 * time.Millisecond)
	manager.mu.Lock()
	count := len(manager.streams)
	manager.mu.Unlock()
	if count != 1 {
		t.Errorf("expected stream to survive target errors, got %d streams", count)
	}
}

func TestStartSyncsImmediately(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	client := &fakeClient{}
	manager := newTestManager(handler, &fakeGate{active: true}, client)
	manager.cfg.SyncInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	waitFor(t, time.Second, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.streams) == 1
	})
}

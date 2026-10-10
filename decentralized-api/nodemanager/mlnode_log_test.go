package nodemanager_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"

	"common/nodemanager/gen"
	commonobs "common/observability"
	"decentralized-api/broker"
	"decentralized-api/nodemanager"
	dapiobs "decentralized-api/observability"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

type captureHandler struct {
	mu      sync.Mutex
	records []map[string]string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	fields := map[string]string{"msg": r.Message, "level": r.Level.String()}
	r.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, fields)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) withStage(stage string) []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]string
	for _, rec := range h.records {
		if rec["stage"] == stage {
			out = append(out, rec)
		}
	}
	return out
}

func captureStageLogs(t *testing.T) *captureHandler {
	t.Helper()
	previousLogger := slog.Default()
	previousFormat := commonobs.LogFormat()
	dapiobs.InstallLogger("json")
	capture := &captureHandler{}
	slog.SetDefault(slog.New(commonobs.NewTraceHandler(capture)))
	t.Cleanup(func() {
		dapiobs.InstallLogger(previousFormat)
		slog.SetDefault(previousLogger)
	})
	return capture
}

type logMockBroker struct {
	acquireFunc func(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error)
	releaseFunc func(lockID string, outcome broker.InferenceResult) (string, error)
}

func (m *logMockBroker) AcquireMLNode(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error) {
	return m.acquireFunc(ctx, model, skipNodeIDs)
}
func (m *logMockBroker) ReleaseMLNode(lockID string, outcome broker.InferenceResult) (string, error) {
	return m.releaseFunc(lockID, outcome)
}
func (m *logMockBroker) TriggerStatusQuery(_ bool)                {}
func (m *logMockBroker) GetNodes() ([]broker.NodeResponse, error) { return nil, nil }

func startTracedGRPC(t *testing.T, srv *nodemanager.Server) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(
		commonobs.UnaryServerTraceInterceptor("decentralized-api.nodemanager"),
	))
	gen.RegisterNodeManagerServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	dial := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dial),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(commonobs.UnaryClientTraceInterceptor()),
	)
	require.NoError(t, err)
	return conn, func() {
		conn.Close()
		gs.Stop()
	}
}

func useTracing(t *testing.T) {
	t.Helper()
	prevProp := otel.GetTextMapPropagator()
	prevTP := otel.GetTracerProvider()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() {
		otel.SetTextMapPropagator(prevProp)
		otel.SetTracerProvider(prevTP)
	})
}

func TestAcquireMLNode_LogsCarryCallerTraceAndRequestID(t *testing.T) {
	useTracing(t)
	capture := captureStageLogs(t)

	srv := nodemanager.NewServer(&logMockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-1", "http://node:8080/v1", "node-1", nil
		},
		releaseFunc: func(_ string, _ broker.InferenceResult) (string, error) {
			return "node-1", nil
		},
	}, nil, nil)
	conn, cleanup := startTracedGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	ctx, span := otel.Tracer("test").Start(context.Background(), "caller")
	ctx = commonobs.SetRequestID(ctx, "req-c8")
	wantTrace := span.SpanContext().TraceID().String()

	acq, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "Qwen/Test", EscrowId: "12"})
	require.NoError(t, err)
	_, err = client.ReleaseMLNode(ctx, &gen.ReleaseMLNodeRequest{
		LockId:  acq.LockId,
		Outcome: gen.ReleaseOutcome_SUCCESS,
	})
	require.NoError(t, err)
	span.End()

	acquires := capture.withStage(nodemanager.StageMLNodeAcquire)
	require.Len(t, acquires, 1)
	require.Equal(t, wantTrace, acquires[0]["trace_id"])
	require.Equal(t, "req-c8", acquires[0]["request_id"])
	require.Equal(t, "acquired", acquires[0]["outcome"])
	require.Equal(t, "INFO", acquires[0]["level"])
	require.Empty(t, acquires[0]["subsystem"])
	require.Equal(t, "node-1", acquires[0]["node_id"])
	require.Equal(t, acq.LockId, acquires[0]["lock_id"])
	require.Equal(t, "Qwen/Test", acquires[0]["model"])
	require.Equal(t, "12", acquires[0]["escrow_id"])
	require.NotEmpty(t, acquires[0]["span_id"])

	releases := capture.withStage(nodemanager.StageMLNodeRelease)
	require.Len(t, releases, 1)
	require.Equal(t, wantTrace, releases[0]["trace_id"])
	require.Equal(t, "req-c8", releases[0]["request_id"])
	require.Equal(t, acq.LockId, releases[0]["lock_id"])
	require.Equal(t, "node-1", releases[0]["node_id"])
	require.Equal(t, "SUCCESS", releases[0]["outcome"])
	require.Equal(t, "true", releases[0]["released"])
	require.Equal(t, "INFO", releases[0]["level"])
}

func TestAcquireMLNode_LogsExhaustedPool(t *testing.T) {
	useTracing(t)
	capture := captureStageLogs(t)

	srv := nodemanager.NewServer(&logMockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "", "", "", broker.ErrNoNodesAvailable
		},
	}, nil, nil)
	conn, cleanup := startTracedGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	_, err := client.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{
		Model:         "Qwen/Test",
		ExcludedNodes: []string{"solo"},
	})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))

	acquires := capture.withStage(nodemanager.StageMLNodeAcquire)
	require.Len(t, acquires, 1)
	require.Equal(t, "no_nodes_available", acquires[0]["outcome"])
	require.Equal(t, "solo", acquires[0]["excluded"])
	require.Equal(t, "ERROR", acquires[0]["level"])
	require.Equal(t, "Nodes", acquires[0]["subsystem"])
}

func TestAcquireMLNode_FailureOutcomesLogAtError(t *testing.T) {
	capture := captureStageLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := nodemanager.NewServer(&logMockBroker{
		acquireFunc: func(callCtx context.Context, _ string, _ []string) (string, string, string, error) {
			if callCtx.Err() != nil {
				return "", "", "", callCtx.Err()
			}
			return "", "", "", errors.New("queue full")
		},
	}, nil, nil)

	_, err := srv.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "Qwen/Test", EscrowId: "12"})
	require.Error(t, err)
	cancelled := capture.withStage(nodemanager.StageMLNodeAcquire)
	require.Len(t, cancelled, 1)
	require.Equal(t, "context_error", cancelled[0]["outcome"])
	require.Equal(t, "ERROR", cancelled[0]["level"])
	require.Equal(t, "Nodes", cancelled[0]["subsystem"])

	_, err = srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "Qwen/Test"})
	require.Equal(t, codes.Unavailable, status.Code(err))
	unavailable := capture.withStage(nodemanager.StageMLNodeAcquire)
	require.Len(t, unavailable, 2)
	require.Equal(t, "unavailable", unavailable[1]["outcome"])
	require.Equal(t, "ERROR", unavailable[1]["level"])
	require.Equal(t, "Nodes", unavailable[1]["subsystem"])
}

func TestReleaseMLNode_FailureOutcomesLogAtError(t *testing.T) {
	capture := captureStageLogs(t)
	srv := nodemanager.NewServer(&logMockBroker{
		releaseFunc: func(lockID string, _ broker.InferenceResult) (string, error) {
			if lockID == "missing" {
				return "", broker.ErrLockNotFound
			}
			return "node-1", errors.New("release failed")
		},
	}, nil, nil)

	_, err := srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{LockId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{LockId: "lock-1"})
	require.Equal(t, codes.Internal, status.Code(err))

	releases := capture.withStage(nodemanager.StageMLNodeRelease)
	require.Len(t, releases, 2)
	require.Equal(t, "ERROR", releases[0]["level"])
	require.Equal(t, "Nodes", releases[0]["subsystem"])
	require.Equal(t, "false", releases[0]["released"])
	require.Equal(t, "missing", releases[0]["lock_id"])
	require.Equal(t, "ERROR", releases[1]["level"])
	require.Equal(t, "Nodes", releases[1]["subsystem"])
	require.Equal(t, "release failed", releases[1]["error"])
}

package params_test

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"

	"common/nodemanager/gen"
	commonobs "common/observability"
	commonruntimeconfig "common/runtimeconfig"
	"devshard/chainoracle/params"
	devshardobs "devshard/observability"

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

// captureHandler records slog attributes so tests can assert on the structured
// fields Loki will index, instead of on rendered text.
type captureHandler struct {
	mu      sync.Mutex
	records []map[string]string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	fields := map[string]string{"msg": r.Message}
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

// captureStageLogs installs a JSON-mode logger that records into the returned
// handler. Going through devshard/observability is deliberate: JSON mode is
// what makes logging.Stage emit structured attrs, and importing that package is
// what registers the request_id context field — the same two things mock-dapi's
// main does at boot.
func captureStageLogs(t *testing.T) *captureHandler {
	t.Helper()
	previousLogger := slog.Default()
	previousFormat := commonobs.LogFormat()
	devshardobs.InstallLogger("json")
	capture := &captureHandler{}
	slog.SetDefault(slog.New(commonobs.NewTraceHandler(capture)))
	t.Cleanup(func() {
		devshardobs.InstallLogger(previousFormat)
		slog.SetDefault(previousLogger)
	})
	return capture
}

func startTracedGRPC(t *testing.T, srv *params.Server) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(
		commonobs.UnaryServerTraceInterceptor("mock-dapi.nodemanager"),
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

func newPoolServer(t *testing.T, nodes ...params.MLNode) *params.Server {
	t.Helper()
	src, err := params.NewCachedSource(context.Background(), nil, commonruntimeconfig.Snapshot{})
	require.NoError(t, err)
	srv, err := params.NewServer(params.Config{Source: src, MLNodes: nodes})
	require.NoError(t, err)
	return srv
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

// TestAcquireMLNode_LogsCarryCallerTraceAndRequestID is the unit-level form of
// C8: the node-selection log lines must join on the caller's ids, not on a
// context the server invented.
func TestAcquireMLNode_LogsCarryCallerTraceAndRequestID(t *testing.T) {
	useTracing(t)
	capture := captureStageLogs(t)

	srv := newPoolServer(t, params.MLNode{ID: "mock-openai-0", Endpoint: "http://mock-openai-0:8088"})
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

	acquires := capture.withStage(params.StageMLNodeAcquire)
	require.Len(t, acquires, 1)
	require.Equal(t, wantTrace, acquires[0]["trace_id"])
	require.Equal(t, "req-c8", acquires[0]["request_id"])
	require.Equal(t, "acquired", acquires[0]["outcome"])
	require.Equal(t, "mock-openai-0", acquires[0]["node_id"])
	require.Equal(t, acq.LockId, acquires[0]["lock_id"])
	require.Equal(t, "Qwen/Test", acquires[0]["model"])
	require.Equal(t, "12", acquires[0]["escrow_id"])
	require.NotEmpty(t, acquires[0]["span_id"])

	releases := capture.withStage(params.StageMLNodeRelease)
	require.Len(t, releases, 1)
	require.Equal(t, wantTrace, releases[0]["trace_id"])
	require.Equal(t, "req-c8", releases[0]["request_id"])
	require.Equal(t, acq.LockId, releases[0]["lock_id"])
	require.Equal(t, "mock-openai-0", releases[0]["node_id"])
	require.Equal(t, "SUCCESS", releases[0]["outcome"])
	require.Equal(t, "true", releases[0]["released"])
}

func TestAcquireMLNode_LogsExhaustedPool(t *testing.T) {
	useTracing(t)
	capture := captureStageLogs(t)

	srv := newPoolServer(t, params.MLNode{ID: "solo", Endpoint: "http://solo:8088"})
	conn, cleanup := startTracedGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	_, err := client.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{
		Model:         "Qwen/Test",
		ExcludedNodes: []string{"solo"},
	})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))

	acquires := capture.withStage(params.StageMLNodeAcquire)
	require.Len(t, acquires, 1)
	require.Equal(t, "no_nodes_available", acquires[0]["outcome"])
	require.Equal(t, "solo", acquires[0]["excluded"])
	require.Equal(t, "1", acquires[0]["pool_size"])
}

// TestReleaseMLNode_LogsUnknownLockUnderCancelledContext covers the two ways a
// release arrives degraded: the lock is already gone, and the caller's context
// is cancelled. Neither may panic or skip the audit line.
func TestReleaseMLNode_LogsUnknownLockUnderCancelledContext(t *testing.T) {
	useTracing(t)
	capture := captureStageLogs(t)

	srv := newPoolServer(t, params.MLNode{ID: "solo", Endpoint: "http://solo:8088"})

	ctx, cancel := context.WithCancel(commonobs.SetRequestID(context.Background(), "req-cancelled"))
	cancel()

	// Call the server directly: a cancelled context never reaches the wire.
	_, err := srv.ReleaseMLNode(ctx, &gen.ReleaseMLNodeRequest{LockId: "missing-lock"})
	require.NoError(t, err)

	releases := capture.withStage(params.StageMLNodeRelease)
	require.Len(t, releases, 1)
	require.Equal(t, "req-cancelled", releases[0]["request_id"])
	require.Equal(t, "missing-lock", releases[0]["lock_id"])
	require.Equal(t, "false", releases[0]["released"])
	require.Empty(t, releases[0]["node_id"])
}

func TestAcquireMLNode_LogsWithoutRequestIDOrTrace(t *testing.T) {
	useTracing(t)
	capture := captureStageLogs(t)

	srv := newPoolServer(t, params.MLNode{ID: "solo", Endpoint: "http://solo:8088"})

	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "Qwen/Test"})
	require.NoError(t, err)

	acquires := capture.withStage(params.StageMLNodeAcquire)
	require.Len(t, acquires, 1)
	require.Empty(t, acquires[0]["trace_id"], "no span on ctx means no trace_id, not a fabricated one")
	require.Empty(t, acquires[0]["request_id"])
	require.Equal(t, "acquired", acquires[0]["outcome"])
}

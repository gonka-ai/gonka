package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	nmgen "common/nodemanager/gen"

	"devshard/observability"

	"github.com/stretchr/testify/require"
)

func TestRenderOnMLNodeReleasesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
	}))
	t.Cleanup(server.Close)

	var released atomic.Bool
	client := startEngineMLClient(t, &engineMockNM{
		acquireFunc: func(context.Context, *nmgen.AcquireMLNodeRequest) (*nmgen.AcquireMLNodeResponse, error) {
			return &nmgen.AcquireMLNodeResponse{LockId: "lock", NodeId: "node", Endpoint: server.URL}, nil
		},
		releaseFunc: func(context.Context, *nmgen.ReleaseMLNodeRequest) (*nmgen.ReleaseMLNodeResponse, error) {
			released.Store(true)
			return &nmgen.ReleaseMLNodeResponse{}, nil
		},
	})
	engine := newTestEngine(client, nil, nil)
	var nodes []string
	ctx = observability.WithRenderNodeObserver(ctx, func(id, endpoint string) { nodes = append(nodes, id+"@"+endpoint) })
	_, _, _, _ = engine.RenderOnMLNode(ctx, "model", "/v1/chat/completions/render", []byte(`{}`))
	require.Equal(t, []string{"node@" + server.URL}, nodes)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Eventually(t, released.Load, time.Second, 10*time.Millisecond, "the acquired node must be released")
}

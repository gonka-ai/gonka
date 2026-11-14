//go:build testenvci

package citest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestMLNodePool_PerNodeFault runs two mock-openai instances behind mock-dapi:
// AcquireMLNode returns distinct NodeIds, and /testenv/fault on one node does
// not affect the other.
func TestMLNodePool_PerNodeFault(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootMLNodePoolStack(t, "citest-ml-pool-*", 2)
	client := harness.HTTPClient()
	t.Cleanup(func() {
		harness.ResetAllMockOpenAIFaults(t, client, stack, cfg)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "mock-dapi", "mock-openai-0", "mock-openai-1", "versiond-0")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)

	ids := harness.MLNodeIDs(cfg)
	require.Equal(t, []string{"mock-openai-0", "mock-openai-1"}, ids)

	harness.Step(t, "AcquireMLNode round-robins distinct node ids")
	conn, err := grpc.NewClient(eps.MockDapiGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	nm := gen.NewNodeManagerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := nm.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: config.PrimaryModelID(cfg)})
	require.NoError(t, err)
	b, err := nm.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: config.PrimaryModelID(cfg)})
	require.NoError(t, err)
	require.NotEqual(t, a.GetNodeId(), b.GetNodeId())
	require.Contains(t, ids, a.GetNodeId())
	require.Contains(t, ids, b.GetNodeId())
	_, _ = nm.ReleaseMLNode(ctx, &gen.ReleaseMLNodeRequest{LockId: a.GetLockId()})
	_, _ = nm.ReleaseMLNode(ctx, &gen.ReleaseMLNodeRequest{LockId: b.GetLockId()})

	harness.Step(t, "latency fault on mock-openai-1 only")
	latency := 1500
	harness.PatchMockOpenAIFaultForNode(t, client, stack, cfg, "mock-openai-1", mockopenai.FaultPatch{
		LatencyMs: &latency,
	})

	fastURL := harness.MockOpenAIHTTPForNode(t, stack, cfg, "mock-openai-0")
	slowURL := harness.MockOpenAIHTTPForNode(t, stack, cfg, "mock-openai-1")

	fastDur := postMockChatDuration(t, client, fastURL)
	slowDur := postMockChatDuration(t, client, slowURL)
	require.Less(t, fastDur, 800*time.Millisecond, "node-0 should stay fast; took %s", fastDur)
	require.GreaterOrEqual(t, slowDur, 1200*time.Millisecond, "node-1 should honor latency_ms; took %s", slowDur)

	harness.Step(t, "StopMLNode yields connection refused on that instance only")
	harness.StopMLNode(t, stack, "mock-openai-1")
	_, err = client.Get(slowURL + "/healthz")
	require.Error(t, err)
	resp, err := client.Get(fastURL + "/healthz")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()
	harness.StartMLNode(t, stack, "mock-openai-1")
}

func postMockChatDuration(t *testing.T, client *http.Client, baseURL string) time.Duration {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "test-model",
		"messages": []map[string]string{
			{"role": "user", "content": "citest ml pool timing"},
		},
		"max_tokens": 8,
	})
	require.NoError(t, err)
	start := time.Now()
	resp, err := client.Post(baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return time.Since(start)
}

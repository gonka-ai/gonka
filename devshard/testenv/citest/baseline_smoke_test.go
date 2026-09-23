//go:build testenvci

package citest

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	// baselineVersiondImage is the Phase 6 B1 pin (testenv/baselines/0.2.15-v5.txt).
	baselineVersiondImage = "devshard-versiond:0.2.15-v5"
	// baselineVersiondRouterImage is the matching router pin.
	baselineVersiondRouterImage = "devshard-versiond-router:0.2.15-v5"
	// baselineBinaryLogVersion is the ldflags value make build-devshardd stamps
	// into the mounted child (README pass line for versiond-0).
	baselineBinaryLogVersion = "0.2.13-v2-r2"
)

// TestBaselineSmoke is P6.8.1: pinned 0.2.15-v5 versiond and router, this
// child, RPC off, no proxy overlay. JSON chat must work when
// GET /devshard/stats/rpc is 404, a pin-to-primary, or a merge.
//
// Unpinned citest-stack skips. make citest-baseline-smoke sets the pin.
// A pinned citest-stack runs this test too (BaselineSmoke is in STACK_CITEST_PATTERN).
func TestBaselineSmoke(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireBaselinePin(t)
	requireRPCOff(t)
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-baseline-smoke-*")
	require.False(t, stack.ProxyOverlay, "P6.8.1 is topology B: no proxy overlay")
	requirePinnedCompose(t, stack)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router", "mock-openai")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	stack.RequireServicesRunning(t, "versiond-0", "versiond-1", "versiond-router", "devshardctl")
	proxyUp, err := stack.ServiceRunning("proxy")
	require.NoError(t, err)
	require.False(t, proxyUp, "proxy service must not be running")
	requireVersiondRPCOff(t, stack)

	version := cfg.Versiond.VersionName
	logs := waitBaselineChildLogs(t, stack, version)
	require.NotContains(t, logs, "protocol mismatch")
	require.NotContains(t, logs, "child exited")

	harness.WaitGETOK(t, client, eps.RouterHTTP+"/healthz", 2*time.Minute, "versiond-router /healthz", stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+version+"/healthz", 2*time.Minute, "devshardd health via router", stack)

	postBaselineChat(t, client, eps, cfg, "citest baseline smoke chat")

	statsURL := eps.RouterHTTP + "/devshard/stats/rpc"
	status, snippet := getRPCStats(t, statsURL)
	t.Logf("P6.8.1 GET %s -> %d %s", statsURL, status, classifyRPCStats(status, snippet))

	// The gauge appears only after the poller has a participant InferenceUrl.
	// A baseline scrape can be rpc_stats_up=0, or the series can still be
	// absent; either way the gateway must keep serving chat.
	metricsURL := eps.GatewayHTTP + "/metrics"
	body := waitRPCStatsUp(t, client, metricsURL, 20*time.Second)
	up := rpcStatsUpLines(body)
	if len(up) == 0 {
		t.Log("P6.8.1 devshard_gateway_rpc_stats_up not published yet")
	}
	for _, line := range up {
		t.Logf("P6.8.1 %s", line)
	}
	running, err := stack.ServiceRunning("devshardctl")
	require.NoError(t, err)
	require.True(t, running, "gateway exited after /devshard/stats/rpc scrape")

	postBaselineChat(t, client, eps, cfg, "citest baseline smoke chat after rpc stats")
}

func requireBaselinePin(t *testing.T) {
	t.Helper()
	versiond := strings.TrimSpace(os.Getenv(harness.EnvVersiondImage))
	router := strings.TrimSpace(os.Getenv(harness.EnvVersiondRouterImage))
	if versiond == "" && router == "" {
		t.Skipf("P6.8.1 needs %s=%s and %s=%s (make -C devshard/testenv citest-baseline-smoke)",
			harness.EnvVersiondImage, baselineVersiondImage,
			harness.EnvVersiondRouterImage, baselineVersiondRouterImage)
	}
	require.Equal(t, baselineVersiondImage, versiond, harness.EnvVersiondImage)
	require.Equal(t, baselineVersiondRouterImage, router, harness.EnvVersiondRouterImage)
}

func requireRPCOff(t *testing.T) {
	t.Helper()
	switch strings.TrimSpace(os.Getenv("DEVSHARD_RPC_SERVER_ENABLED")) {
	case "", "false", "0":
	default:
		// Pinned citest-stack also runs this test. §8.2 RPC columns must not
		// fail the RPC-off smoke.
		t.Skipf("P6.8.1 is RPC off; DEVSHARD_RPC_SERVER_ENABLED=%q", os.Getenv("DEVSHARD_RPC_SERVER_ENABLED"))
	}
}

func requirePinnedCompose(t *testing.T, stack *harness.Stack) {
	t.Helper()
	body, err := os.ReadFile(stack.ComposePath)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "image: "+baselineVersiondImage)
	require.Contains(t, text, "image: "+baselineVersiondRouterImage)
	require.NotContains(t, text, "image: devshard-versiond:latest")
	require.NotContains(t, text, "image: devshard-versiond-router:latest")
	require.Contains(t, text, "DEVSHARD_RPC_SERVER_ENABLED: ${DEVSHARD_RPC_SERVER_ENABLED:-false}")
	require.NotContains(t, text, "docker-compose.proxy.yml")
}

func requireVersiondRPCOff(t *testing.T, stack *harness.Stack) {
	t.Helper()
	out, err := stack.ComposeExecOutput("versiond-0", "printenv", "DEVSHARD_RPC_SERVER_ENABLED")
	require.NoError(t, err)
	require.Equal(t, "false", strings.TrimSpace(out))
}

func waitBaselineChildLogs(t *testing.T, stack *harness.Stack, version string) string {
	t.Helper()
	protocol := "protocol_version=" + version
	binary := "binary_log_version=" + baselineBinaryLogVersion
	var last string
	ok := harness.AssertEventually(t, 3*time.Minute, 2*time.Second, func() bool {
		out, err := stack.ComposeLogsAll("versiond-0")
		if err != nil {
			last = err.Error()
			return false
		}
		last = out
		return strings.Contains(out, "using override binary") &&
			strings.Contains(out, "version="+version) &&
			strings.Contains(out, "devshardd starting") &&
			strings.Contains(out, protocol) &&
			strings.Contains(out, binary)
	})
	require.True(t, ok, "versiond-0 logs missing override binary / %s / %s within 3m\n%s", protocol, binary, last)
	return last
}

func postBaselineChat(t *testing.T, client *http.Client, eps harness.Endpoints, cfg *config.File, content string) {
	t.Helper()
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute)
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: content},
		},
		MaxTokens: 32,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
}

func getRPCStats(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := harness.HTTPClient().Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	require.NoError(t, err)
	return resp.StatusCode, strings.TrimSpace(string(body))
}

func classifyRPCStats(status int, body string) string {
	switch status {
	case http.StatusNotFound:
		return "404 (baseline versiond has no /devshard/stats/rpc merge)"
	case http.StatusOK:
		if strings.Contains(body, `"minute_unix"`) {
			return "200 json snapshot (pin-to-primary or merge)"
		}
		return "200"
	default:
		return "recorded non-200"
	}
}

func waitRPCStatsUp(t *testing.T, client *http.Client, metricsURL string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	body := harness.FetchMetricsText(t, client, metricsURL)
	for len(rpcStatsUpLines(body)) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		body = harness.FetchMetricsText(t, client, metricsURL)
	}
	return body
}

func rpcStatsUpLines(body string) []string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "devshard_gateway_rpc_stats_up") {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

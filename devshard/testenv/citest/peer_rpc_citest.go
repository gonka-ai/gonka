//go:build testenvci

package citest

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// peerRPCEndpoints is the §8.2 "RPC on" set (HTTP/1.1, no proxy).
const peerRPCEndpoints = "signatures,mempool,diffs,gossip,repair,height-sync,verify-timeout,verify-error-miss,challenge-receipt,payload,chat"

func requireNoProxyRPC(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_SERVER_ENABLED")) != "true" {
		t.Fatalf("set DEVSHARD_RPC_SERVER_ENABLED=true (make citest-peerrpc-chat / citest-mixed-fleet)")
	}
	got := os.Getenv("DEVSHARD_RPC_ENDPOINTS")
	for _, name := range strings.Split(peerRPCEndpoints, ",") {
		if !rpcEndpointListed(got, name) {
			t.Fatalf("DEVSHARD_RPC_ENDPOINTS=%q missing %q", got, name)
		}
	}
	if harness.ProxyOverlayFromEnv() {
		if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_PORT")) == "" {
			t.Fatal("§9.1 sets DEVSHARD_RPC_H2_PORT")
		}
		if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_HOST")) != "proxy" {
			t.Fatalf("§9.1 DEVSHARD_RPC_H2_HOST=%q, want proxy", os.Getenv("DEVSHARD_RPC_H2_HOST"))
		}
		upgrade := strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_UPGRADE"))
		if !strings.EqualFold(upgrade, "true") && upgrade != "1" {
			t.Fatal("§9.1 sets DEVSHARD_RPC_H2_UPGRADE=true")
		}
		return
	}
	if p := strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_PORT")); p != "" {
		t.Fatalf("§8.2 is HTTP/1.1 on :8080; DEVSHARD_RPC_H2_PORT=%q", p)
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_UPGRADE")), "true") {
		t.Fatal("§8.2 is HTTP/1.1 on :8080; DEVSHARD_RPC_H2_UPGRADE must be off")
	}
}

// requireNoProxyGRPC is the current-image no-proxy dial: native gRPC on
// versiond-router:8081. No proxy container. :8080 stays healthz and ops.
func requireNoProxyGRPC(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_SERVER_ENABLED")) != "true" {
		t.Fatal("set DEVSHARD_RPC_SERVER_ENABLED=true")
	}
	got := os.Getenv("DEVSHARD_RPC_ENDPOINTS")
	for _, name := range strings.Split(peerRPCEndpoints, ",") {
		if !rpcEndpointListed(got, name) {
			t.Fatalf("DEVSHARD_RPC_ENDPOINTS=%q missing %q", got, name)
		}
	}
	if harness.ProxyOverlayFromEnv() {
		t.Fatal("no-proxy gRPC must not set TESTENV_PROXY_OVERLAY")
	}
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_PORT")) != "8081" {
		t.Fatalf("DEVSHARD_RPC_H2_PORT=%q, want versiond-router h2 port 8081", os.Getenv("DEVSHARD_RPC_H2_PORT"))
	}
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_HOST")) != "" {
		t.Fatalf("DEVSHARD_RPC_H2_HOST=%q, want empty so the dial stays on the InferenceUrl host", os.Getenv("DEVSHARD_RPC_H2_HOST"))
	}
	upgrade := strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_UPGRADE"))
	if !strings.EqualFold(upgrade, "true") && upgrade != "1" {
		t.Fatal("set DEVSHARD_RPC_H2_UPGRADE=true")
	}
	if strings.TrimSpace(os.Getenv("DEVSHARD_RPC_H2_FRONT_HOST")) != "versiond-router" {
		t.Fatalf("DEVSHARD_RPC_H2_FRONT_HOST=%q, want versiond-router", os.Getenv("DEVSHARD_RPC_H2_FRONT_HOST"))
	}
	grpc := strings.TrimSpace(os.Getenv("DEVSHARD_RPC_GRPC"))
	if !strings.EqualFold(grpc, "true") && grpc != "1" {
		t.Fatal("set DEVSHARD_RPC_GRPC=true")
	}
}

func rpcEndpointListed(list, name string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}

func requireNoProxyService(t *testing.T, stack *harness.Stack) {
	t.Helper()
	up, err := stack.ServiceRunning("proxy")
	require.NoError(t, err)
	if harness.ProxyOverlayFromEnv() {
		require.True(t, stack.ProxyOverlay, "§9.1 boots the proxy overlay")
		require.True(t, up, "§9.1 proxy must be running")
		return
	}
	require.False(t, stack.ProxyOverlay)
	require.False(t, up, "§8.2 must not start proxy")
}

func postGatewayChats(t *testing.T, client *http.Client, eps harness.Endpoints, cfg *config.File, content string) {
	t.Helper()
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: content}},
		MaxTokens: 32,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	stream, _ := harness.PostGatewayChatCompletionStream(t, client, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: content + " stream"}},
		MaxTokens: 32,
		Stream:    true,
	})
	harness.RequireMockOpenAIContent(t, stream)
}

// hostMetricBodies scrapes each versiond's /{version}/metrics through that
// host's own :8080 (not the sticky router).
func hostMetricBodies(t *testing.T, stack *harness.Stack, cfg *config.File) map[string]string {
	t.Helper()
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	out := make(map[string]string, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		body, err := stack.ComposeExecOutput(h.ID, "wget", "-qO-", "http://127.0.0.1:8080/"+version+"/metrics")
		if err != nil {
			t.Logf("citest: %s metrics: %v", h.ID, err)
			continue
		}
		out[h.ID] = body
	}
	return out
}

func metricLine(body, name string, parts ...string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, name) {
			continue
		}
		ok := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func hostWithMetric(bodies map[string]string, name string, parts ...string) string {
	for id, body := range bodies {
		if metricLine(body, name, parts...) {
			return id
		}
	}
	return ""
}

func waitHostMetric(t *testing.T, stack *harness.Stack, cfg *config.File, timeout time.Duration, name string, parts ...string) (host string, bodies map[string]string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		bodies = hostMetricBodies(t, stack, cfg)
		if host = hostWithMetric(bodies, name, parts...); host != "" {
			return host, bodies
		}
		if time.Now().After(deadline) {
			return "", bodies
		}
		time.Sleep(2 * time.Second)
	}
}

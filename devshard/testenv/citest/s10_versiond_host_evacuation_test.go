//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// TestS10_VersiondHostEvacuation verifies the complete Track B transaction:
// nginx removes one versiond from admission without breaking its established
// stream, versiond exits only after that stream drains, and a replacement stays
// down until it is healthy and explicitly activated.
func TestS10_VersiondHostEvacuation(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	env := bootS9RollingStack(t, "citest-s10-*", true, func(stack *harness.Stack, cfg *config.File) {
		hosts := []string{cfg.Hosts[0].ID, cfg.Hosts[1].ID}
		harness.PatchRouterVersiondHosts(t, stack.ComposePath, strings.Join(hosts, " "))
		harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", `""`)
	})
	client := harness.GatewayChatClient()
	escrowID := harness.GetGatewayEscrowID(t, client, env.eps.GatewayHTTP)
	targetUpstream := harness.RequireResponseHeader(
		t,
		client,
		harness.RouterSessionURL(
			env.eps.RouterHTTP,
			env.cfg.Versiond.VersionName,
			escrowID,
			"/healthz",
		),
		harness.StickyUpstreamHeader,
	)
	targetHost := harness.HostIDForUpstream(env.cfg, targetUpstream)
	require.Contains(t, env.hosts, targetHost)
	survivorHost := env.hosts[0]
	if survivorHost == targetHost {
		survivorHost = env.hosts[1]
	}
	evacuationID := "s10-evacuate-" + targetHost
	replacementID := "s10-replace-" + targetHost

	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, env.stack, targetHost, survivorHost, "versiond-router", "devshardctl")
		}
	})

	delayMs := 750
	harness.PatchMockOpenAIFault(t, client, env.eps.MockOpenAIHTTP, mockopenai.FaultPatch{
		StreamChunkDelay: &delayMs,
	})

	accepted, streamResult := harness.StartGatewayChatCompletionStream(
		client,
		env.eps.GatewayHTTP,
		harness.TestenvAdminAPIKey,
		harness.ChatCompletionRequest{
			Model:     "test-model",
			MaxTokens: 64,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: "s10 long stream across versiond host evacuation",
			}},
		},
	)
	requireS9StreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "marking the stream's target %s down with nginx reload", targetHost)
	routerHostTransition(t, env.stack, "drain", targetHost, evacuationID)

	stopResult := make(chan error, 1)
	go func() {
		stopResult <- env.stack.StopServiceGracefully(targetHost, 2*time.Minute)
	}()

	harness.Step(t, "versiond enters draining while the established stream remains active")
	observedDraining := harness.AssertEventually(t, 30*time.Second, 250*time.Millisecond, func() bool {
		summary, err := harness.TryVersiondHealthSummary(env.stack, targetHost)
		return err == nil && summary.State == "draining" && summary.ProxyInflight > 0
	})
	require.True(t, observedDraining, "target versiond did not expose draining host inflight")
	running, err := env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.True(t, running, "target versiond exited before its accepted stream completed")
	requireS9StreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "new router requests avoid the draining upstream")
	for i := 0; i < 16; i++ {
		upstream := harness.RequireResponseHeader(
			t,
			client,
			harness.RouterSessionURL(
				env.eps.RouterHTTP,
				env.cfg.Versiond.VersionName,
				fmt.Sprintf("s10-new-%d", i),
				"/healthz",
			),
			harness.StickyUpstreamHeader,
		)
		require.NotEqual(t, targetHost, harness.HostIDForUpstream(env.cfg, upstream))
	}
	healthURL := env.eps.RouterHTTP + "/" + env.cfg.Versiond.VersionName + "/healthz"
	healthResp, err := client.Get(healthURL)
	require.NoError(t, err)
	defer healthResp.Body.Close()
	require.Equal(t, http.StatusOK, healthResp.StatusCode)
	require.Equal(
		t,
		survivorHost,
		harness.HostIDForUpstream(env.cfg, healthResp.Header.Get(harness.StickyUpstreamHeader)),
	)

	harness.Step(t, "old stream completes before versiond exits")
	result := <-streamResult
	require.NoError(t, result.Err)
	require.Equal(t, http.StatusOK, result.Status, "stream body: %s", result.Body)
	require.True(t, result.SawDone, "stream missing [DONE]")
	harness.RequireMockOpenAIContent(t, result.Content)
	select {
	case err := <-stopResult:
		require.NoError(t, err)
	case <-time.After(90 * time.Second):
		t.Fatal("versiond did not exit after its host became idle")
	}
	running, err = env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.False(t, running, "target versiond is still running after graceful stop")
	routerHostTransition(t, env.stack, "offline", targetHost, evacuationID)

	harness.Step(t, "replacement remains joining/down until health is ready")
	routerHostTransition(t, env.stack, "join", targetHost, replacementID)
	env.stack.StartService(t, targetHost)
	ready := harness.AssertEventually(t, 2*time.Minute, time.Second, func() bool {
		summary, err := harness.TryVersiondHealthSummary(env.stack, targetHost)
		return err == nil && summary.State == "serving" && summary.Ready && summary.Accepting
	})
	require.True(t, ready, "replacement versiond did not become ready")
	routerHostTransition(t, env.stack, "activate", targetHost, replacementID)

	rehashedToReplacement := harness.AssertEventually(t, 30*time.Second, 100*time.Millisecond, func() bool {
		for i := 0; i < 128; i++ {
			upstream, err := harness.GetResponseHeader(
				client,
				harness.RouterSessionURL(
					env.eps.RouterHTTP,
					env.cfg.Versiond.VersionName,
					fmt.Sprintf("s10-rejoin-%d", i),
					"/healthz",
				),
				harness.StickyUpstreamHeader,
			)
			if err == nil && harness.HostIDForUpstream(env.cfg, upstream) == targetHost {
				return true
			}
		}
		return false
	})
	require.True(t, rehashedToReplacement, "activated replacement never received a sticky assignment")
}

func routerHostTransition(t *testing.T, stack *harness.Stack, action, host, operationID string) {
	t.Helper()
	out, err := stack.ComposeExecOutput(
		"versiond-router",
		"gonka-routerctl",
		"host",
		action,
		"--operation-id",
		operationID,
		host,
	)
	require.NoError(t, err, "router host %s %s: %s", action, host, out)
}

//go:build testenvci

package citest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

const (
	hostEvacuationShutdownBudget     = 90 * time.Second
	hostEvacuationObservationTimeout = 60 * time.Second
	hostReplacementReadyTimeout      = 3 * time.Minute
)

// TestVersiondHostEvacuation verifies the whole Track B host lifecycle with no
// control plane in it: stopping a versiond takes it out of rotation before it
// stops accepting, its established stream still finishes, the session is
// recoverable on the survivor, and starting the host again puts it back into
// the pool once — and only once — it reports ready.
//
// Everything the operator does here is `docker compose stop` and
// `docker compose start`. Membership is DNS and health is measured, so there is
// nothing else to keep in sync.
func TestVersiondHostEvacuation(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	env := bootVersiondRollingStack(t, "citest-versiond-host-evacuation-*", true, func(stack *harness.Stack, cfg *config.File) {
		harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", `""`)
		harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_HOST_SHUTDOWN_BUDGET",
			`"`+hostEvacuationShutdownBudget.String()+`"`)
	})
	client := harness.GatewayChatClient()
	escrowID := harness.GetGatewayEscrowID(t, client, env.eps.GatewayHTTP)
	sessionURL := harness.RouterSessionURL(
		env.eps.RouterHTTP,
		env.cfg.Versiond.VersionName,
		escrowID,
		"/mempool",
	)
	targetUpstream := harness.RequireSuccessfulResponseHeader(
		t, client, sessionURL, harness.StickyUpstreamHeader)
	targetHost := harness.HostIDForUpstream(env.cfg, targetUpstream)
	require.Contains(t, env.hosts, targetHost)
	survivorHost := env.hosts[0]
	if survivorHost == targetHost {
		survivorHost = env.hosts[1]
	}

	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, env.stack, targetHost, survivorHost, "versiond-router", "devshardctl")
		}
	})

	harness.Step(t, "both hosts are in the router pool before evacuation")
	require.ElementsMatch(t, env.hosts, harness.RouterServingHosts(t, env.stack, env.cfg),
		"pool: %s", harness.DescribeRouterPool(t, env.stack, env.cfg))

	pauseStream := true
	harness.PatchMockOpenAIFault(t, client, env.eps.MockOpenAIHTTP, mockopenai.FaultPatch{
		PauseStream: &pauseStream,
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
				Content: "long stream across versiond host evacuation",
			}},
		},
	)
	requireVersiondStreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "evacuating %s with docker compose stop", targetHost)
	// Nothing may fail while the host leaves: the announce window exists so the
	// router observes the failing health check before versiond stops accepting.
	probeCtx, stopProbe := context.WithCancel(context.Background())
	probeErr := startRouterContinuityProbe(probeCtx, client,
		env.eps.RouterHTTP+"/"+env.cfg.Versiond.VersionName+"/healthz")
	defer stopProbe()

	evacuationResult := make(chan error, 1)
	go func() {
		evacuationResult <- env.stack.StopServiceGracefully(targetHost, hostEvacuationShutdownBudget)
	}()

	harness.Step(t, "the router stops routing to %s while it is still serving", targetHost)
	harness.WaitRouterPoolState(t, env.stack, env.cfg, targetHost,
		harness.RouterSlotDown, hostEvacuationObservationTimeout)
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost)

	harness.Step(t, "versiond stays alive while its internal FSM drains the established stream")
	running, err := env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.True(t, running, "target versiond exited before its accepted stream completed")
	requireVersiondStreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "the same escrow is recovered on the surviving host")
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost)

	harness.Step(t, "releasing the old stream before versiond exits")
	harness.ReleaseMockOpenAIStreams(t, client, env.eps.MockOpenAIHTTP)
	result := <-streamResult
	require.NoError(t, result.Err)
	require.Equal(t, http.StatusOK, result.Status, "stream body: %s", result.Body)
	require.True(t, result.SawDone, "stream missing [DONE]")
	harness.RequireMockOpenAIContent(t, result.Content)

	select {
	case err := <-evacuationResult:
		require.NoError(t, err)
	case <-time.After(hostEvacuationShutdownBudget + 30*time.Second):
		t.Fatal("versiond did not exit after its host became idle")
	}
	running, err = env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.False(t, running, "target versiond is still running after graceful stop")

	stopProbe()
	select {
	case err := <-probeErr:
		require.NoError(t, err, "traffic failed while the host was leaving the pool")
	case <-time.After(5 * time.Second):
		t.Fatal("continuity probe did not stop")
	}

	harness.Step(t, "a decommissioned host simply stops resolving; no router change")
	harness.WaitRouterPoolState(t, env.stack, env.cfg, targetHost, "", hostEvacuationObservationTimeout)
	require.Equal(t, []string{survivorHost}, harness.RouterServingHosts(t, env.stack, env.cfg))
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost)

	harness.Step(t, "the last serving host cannot be drained away")
	harness.RequireRouterRefusesToEmptyPool(t, env.stack, survivorHost)
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost)

	harness.Step(t, "restarting %s: it rejoins the pool only after it reports ready", targetHost)
	env.stack.StartService(t, targetHost)
	// A host that is up but still converging must not be routed to: it appears
	// in DNS immediately and in the pool as DOWN until /readyz turns 200.
	harness.WaitRouterPoolState(t, env.stack, env.cfg, targetHost,
		harness.RouterSlotDown, hostEvacuationObservationTimeout)
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost)

	harness.WaitRouterPoolState(t, env.stack, env.cfg, targetHost,
		harness.RouterSlotUp, hostReplacementReadyTimeout)
	require.NoError(t, harness.TryVersiondReady(env.stack, targetHost))

	harness.Step(t, "consistent hashing returns the escrow to %s", targetHost)
	requireSessionAvailableOnHost(t, env, escrowID, targetHost)
}

func requireSessionAvailableOnHost(
	t *testing.T,
	env versiondRollingTestStack,
	escrowID string,
	wantHost string,
) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	available := harness.AssertEventually(t, hostEvacuationObservationTimeout, 250*time.Millisecond, func() bool {
		resp, err := client.Get(harness.RouterSessionURL(
			env.eps.RouterHTTP,
			env.cfg.Versiond.VersionName,
			escrowID,
			"/mempool",
		))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK &&
			harness.HostIDForUpstream(
				env.cfg,
				resp.Header.Get(harness.StickyUpstreamHeader),
			) == wantHost
	})
	require.True(t, available, "escrow %s was not available on %s", escrowID, wantHost)
}

func requireNewRouterRequestsAvoidHost(
	t *testing.T,
	client *http.Client,
	env versiondRollingTestStack,
	escrowID string,
	targetHost string,
) {
	t.Helper()
	avoided := harness.AssertEventually(t, hostEvacuationObservationTimeout, 100*time.Millisecond, func() bool {
		for i := 0; i < 4; i++ {
			upstream, err := harness.GetSuccessfulResponseHeader(
				client,
				harness.RouterSessionURL(
					env.eps.RouterHTTP,
					env.cfg.Versiond.VersionName,
					escrowID,
					"/mempool",
				),
				harness.StickyUpstreamHeader,
			)
			if err != nil || harness.HostIDForUpstream(env.cfg, upstream) == targetHost {
				return false
			}
		}
		return true
	})
	require.True(t, avoided, "new router requests still reached %s", targetHost)
}

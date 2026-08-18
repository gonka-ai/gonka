//go:build testenvci

package citest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

const (
	hostEvacuationShutdownBudget = 90 * time.Second
	// Docker's SIGKILL backstop must sit outside versiond's own shutdown
	// budget. The exit-code assertion below proves that this reserve was not
	// consumed by a stuck process.
	hostEvacuationDockerStopGrace    = 2 * time.Minute
	hostEvacuationObservationTimeout = 60 * time.Second
	hostReplacementReadyTimeout      = 3 * time.Minute
	// All checks made while the old stream is deliberately paused share this
	// one budget. They must release the stream with enough of versiond's own
	// shutdown budget left for request completion and child reaping.
	hostEvacuationPreReleaseBudget       = hostEvacuationShutdownBudget - 30*time.Second
	hostEvacuationStreamCompletionBudget = hostEvacuationShutdownBudget -
		15*time.Second
	hostEvacuationGracefulExitBudget = hostEvacuationShutdownBudget +
		15*time.Second
	// Refused instantly inside the container, so the host under barrier fails
	// its reconcile immediately and keeps failing it.
	unreachableOracleURL = "http://127.0.0.1:1/versions"
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
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, env.stack,
				env.hosts[0], env.hosts[1], "versiond-router", "devshardctl")
		}
	})
	// Declared versions are routed and health-checked through their own pool, so
	// that is the pool whose view of a host the test has to read.
	version := env.cfg.Versiond.VersionName
	pool := harness.VersionPoolBackend(version)
	escrowID := harness.GetGatewayEscrowID(t, client, env.eps.GatewayHTTP)
	harness.Step(t, "binding escrow %s through the owner chat path", escrowID)
	harness.PostGatewayChatCompletion(t, client, env.eps.GatewayHTTP,
		harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
			Model:     "test-model",
			MaxTokens: 16,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: "initialize the versiond host evacuation session",
			}},
		})
	stickySessionURL := func() string {
		return harness.RouterSessionURL(
			env.eps.RouterHTTP,
			env.cfg.Versiond.VersionName,
			escrowID,
			"/mempool",
		)
	}
	targetUpstream := harness.RequireSuccessfulResponseHeader(
		t, client, stickySessionURL(), harness.StickyUpstreamHeader)
	targetHost := harness.HostIDForUpstream(env.cfg, targetUpstream)
	require.Contains(t, env.hosts, targetHost)
	survivorHost := env.hosts[0]
	if survivorHost == targetHost {
		survivorHost = env.hosts[1]
	}

	harness.Step(t, "both hosts are in the router pool before evacuation")
	require.ElementsMatch(t, env.hosts, harness.RouterServingHosts(t, env.stack, env.cfg, pool),
		"pool: %s", harness.DescribeRouterPool(t, env.stack, env.cfg))

	// A restarted router rebuilds the pool from a fresh DNS answer, and nothing
	// guarantees the addresses come back in the same order. If ring position
	// followed the slot they landed in rather than the host itself, every
	// session would silently re-home on a routine router restart.
	//
	// This exercises a real restart but cannot force the adverse ordering, so it
	// is a smoke test rather than the proof; the proof is deterministic and
	// lives in `make -C versiond-router test-hash-ring`.
	harness.Step(t, "restarting the router must not re-home %s", escrowID)
	env.stack.StopService(t, "versiond-router")
	env.stack.StartService(t, "versiond-router")
	// The harness publishes random host ports, and Docker picks a new one when
	// the container comes back, so every endpoint has to be re-resolved.
	env.eps = env.stack.Endpoints(t, env.cfg)
	harness.WaitRouterPoolState(t, env.stack, env.cfg, pool, targetHost,
		harness.RouterSlotUp, hostEvacuationObservationTimeout)
	requireSessionAvailableOnHost(t, env, escrowID, targetHost,
		hostEvacuationObservationTimeout)

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
	// Use the target escrow's sticky path: the same URL was resolved to targetHost
	// above, so this probe necessarily crosses the host being evacuated rather
	// than whichever host owns a constant version-level health path.
	probeURL := stickySessionURL()
	probeUpstream := harness.RequireSuccessfulResponseHeader(
		t, client, probeURL, harness.StickyUpstreamHeader)
	require.Equal(t, targetHost, harness.HostIDForUpstream(env.cfg, probeUpstream),
		"continuity probe is not pinned to the host selected for evacuation")
	probeCtx, stopProbe := context.WithCancel(context.Background())
	probeErr, err := startHostEvacuationContinuityProbe(probeCtx, client, probeURL)
	require.NoError(t, err)
	defer stopProbe()

	type stopOutcome struct {
		result harness.ServiceStopResult
		err    error
	}
	evacuationResult := make(chan stopOutcome, 1)
	evacuationStarted := time.Now()
	preReleaseDeadline := evacuationStarted.Add(hostEvacuationPreReleaseBudget)
	streamCompletionDeadline := evacuationStarted.Add(hostEvacuationStreamCompletionBudget)
	gracefulExitDeadline := evacuationStarted.Add(hostEvacuationGracefulExitBudget)
	go func() {
		result, stopErr := env.stack.StopServiceGracefully(targetHost, hostEvacuationDockerStopGrace)
		evacuationResult <- stopOutcome{result: result, err: stopErr}
	}()

	harness.Step(t, "the router stops routing to %s while it is still serving", targetHost)
	harness.WaitRouterPoolState(t, env.stack, env.cfg, pool, targetHost,
		harness.RouterSlotDown, remainingUntil(t, preReleaseDeadline, "router drain announcement"))
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost,
		remainingUntil(t, preReleaseDeadline, "new-request failover"))

	harness.Step(t, "versiond stays alive while its internal FSM drains the established stream")
	running, err := env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.True(t, running, "target versiond exited before its accepted stream completed")
	requireVersiondStreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "the same escrow is recovered on the surviving host")
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost,
		remainingUntil(t, preReleaseDeadline, "survivor session recovery"))

	harness.Step(t, "releasing the old stream before versiond exits")
	releaseClient := *client
	releaseClient.Timeout = remainingUntil(t, streamCompletionDeadline, "stream release request")
	harness.ReleaseMockOpenAIStreams(t, &releaseClient, env.eps.MockOpenAIHTTP)
	var result harness.GatewayStreamResult
	select {
	case result = <-streamResult:
	case <-time.After(remainingUntil(t, streamCompletionDeadline, "old stream completion")):
		t.Fatal("accepted stream did not finish inside the versiond shutdown budget")
	}
	require.NoError(t, result.Err)
	require.Equal(t, http.StatusOK, result.Status, "stream body: %s", result.Body)
	require.True(t, result.SawDone, "stream missing [DONE]")
	harness.RequireMockOpenAIContent(t, result.Content)

	select {
	case outcome := <-evacuationResult:
		require.NoError(t, outcome.err)
		require.Equal(t, 0, outcome.result.ExitCode,
			"versiond did not complete its handled SIGTERM shutdown; container %s exited with code %d",
			outcome.result.ContainerID, outcome.result.ExitCode)
	case <-time.After(remainingUntil(t, gracefulExitDeadline, "versiond graceful exit")):
		t.Fatal("versiond did not exit before the Docker SIGKILL backstop")
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
	harness.WaitRouterPoolState(t, env.stack, env.cfg, pool, targetHost, "", hostEvacuationObservationTimeout)
	require.Equal(t, []string{survivorHost}, harness.RouterServingHosts(t, env.stack, env.cfg, pool))
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost,
		hostEvacuationObservationTimeout)

	harness.Step(t, "%s comes back up but cannot converge: it must stay out of the pool", targetHost)
	// A host that is up but not yet able to serve must not be routed to. Racing
	// a normal start is no way to check that — a fast child wins and the window
	// never gets observed — so the host is brought back behind a barrier it
	// cannot pass: an oracle that refuses connections. It boots, appears in DNS
	// and stays there, but never learns what to run, so /readyz keeps failing
	// for exactly as long as the test holds the barrier up.
	//
	// Only this host is repointed. The survivor keeps its oracle and keeps
	// serving, which also pins that one host's trouble does not become the
	// pool's.
	oracleURL := harness.PatchComposeServiceEnv(t, env.stack.ComposePath, targetHost,
		"VERSIOND_ORACLE_URL", unreachableOracleURL)
	env.stack.RecreateServices(t, targetHost)

	harness.WaitRouterPoolState(t, env.stack, env.cfg, pool, targetHost,
		harness.RouterSlotDown, hostEvacuationObservationTimeout)
	require.Error(t, harness.TryVersiondReady(env.stack, targetHost, version),
		"%s should report unready while it cannot reach its oracle", targetHost)
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost,
		hostEvacuationObservationTimeout)
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost,
		hostEvacuationObservationTimeout)

	harness.Step(t, "lifting the barrier: %s rejoins once it reports ready", targetHost)
	harness.PatchComposeServiceEnv(t, env.stack.ComposePath, targetHost,
		"VERSIOND_ORACLE_URL", oracleURL)
	env.stack.RecreateServices(t, targetHost)

	harness.WaitRouterPoolState(t, env.stack, env.cfg, pool, targetHost,
		harness.RouterSlotUp, hostReplacementReadyTimeout)
	require.NoError(t, harness.TryVersiondReady(env.stack, targetHost, version))

	harness.Step(t, "consistent hashing returns the escrow to %s", targetHost)
	requireSessionAvailableOnHost(t, env, escrowID, targetHost,
		hostEvacuationObservationTimeout)
}

func requireSessionAvailableOnHost(
	t *testing.T,
	env versiondRollingTestStack,
	escrowID string,
	wantHost string,
	timeout time.Duration,
) {
	t.Helper()
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	if timeout < client.Timeout {
		client.Timeout = timeout
	}
	deadline := time.Now().Add(timeout)
	available := harness.AssertEventually(t, timeout, 250*time.Millisecond, func() bool {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		client.Timeout = min(5*time.Second, remaining)
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
	timeout time.Duration,
) {
	t.Helper()
	probeClient := *client
	deadline := time.Now().Add(timeout)
	avoided := harness.AssertEventually(t, timeout, 100*time.Millisecond, func() bool {
		for i := 0; i < 4; i++ {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false
			}
			probeClient.Timeout = min(5*time.Second, remaining)
			upstream, err := harness.GetSuccessfulResponseHeader(
				&probeClient,
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

func remainingUntil(t *testing.T, deadline time.Time, stage string) time.Duration {
	t.Helper()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatalf("host evacuation exceeded its shared deadline before %s", stage)
	}
	return remaining
}

func startHostEvacuationContinuityProbe(
	ctx context.Context,
	client *http.Client,
	url string,
) (<-chan error, error) {
	probe := func() error {
		resp, err := client.Get(url)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("continuity probe %s: HTTP %d", url, resp.StatusCode)
		}
		return nil
	}
	if err := probe(); err != nil {
		return nil, err
	}

	errCh := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				errCh <- nil
				return
			case <-ticker.C:
				if err := probe(); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()
	return errCh, nil
}

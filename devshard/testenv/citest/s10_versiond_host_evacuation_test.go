//go:build testenvci

package citest

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

const (
	s10DrainTimeout       = 2 * time.Minute
	s10KillGrace          = 2 * time.Minute
	s10CommandTimeout     = 30 * time.Second
	s10ObservationTimeout = 60 * time.Second
	s10OperationTimeout   = s10DrainTimeout + s10KillGrace + 2*s10CommandTimeout
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
	targetUpstream := harness.RequireSuccessfulResponseHeader(
		t,
		client,
		harness.RouterSessionURL(
			env.eps.RouterHTTP,
			env.cfg.Versiond.VersionName,
			escrowID,
			"/mempool",
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
	hostctl := env.stack.BuildHostctl(t)
	routerContainer := env.stack.ContainerID(t, "versiond-router")
	targetContainer := env.stack.ContainerID(t, targetHost)
	nginxConfig := env.stack.ComposeExec(t, "versiond-router", "nginx", "-T")
	require.Contains(t, nginxConfig, "proxy_read_timeout 1200s;")
	require.Contains(t, nginxConfig, "proxy_send_timeout 1200s;")

	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, env.stack, targetHost, survivorHost, "versiond-router", "devshardctl")
		}
	})

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
				Content: "s10 long stream across versiond host evacuation",
			}},
		},
	)
	requireS9StreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "interrupting and canceling a checkpointed evacuation")
	cancelID := "s10-cancel-" + targetHost
	cancelJournal := filepath.Join(env.stack.WorkDir, cancelID+".json")
	cancelCtx, cancelOperation := context.WithCancel(context.Background())
	canceledRun := make(chan error, 1)
	go func() {
		_, err := env.stack.RunHostctl(
			cancelCtx,
			hostctl,
			"evacuate",
			hostctlArgs(routerContainer, targetContainer, targetHost, cancelID, cancelJournal)...,
		)
		canceledRun <- err
	}()
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost)
	cancelOperation()
	select {
	case <-canceledRun:
	case <-time.After(15 * time.Second):
		t.Fatal("interrupted hostctl process did not exit")
	}
	cancelCtx, cancelCancel := context.WithTimeout(context.Background(), s10OperationTimeout)
	output, err := env.stack.RunHostctl(
		cancelCtx,
		hostctl,
		"cancel",
		hostctlArgs(routerContainer, targetContainer, targetHost, cancelID, cancelJournal)...,
	)
	cancelCancel()
	require.NoError(t, err, "cancel checkpointed evacuation: %s", output)
	restored := harness.AssertEventually(t, s10ObservationTimeout, 100*time.Millisecond, func() bool {
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
		return err == nil && harness.HostIDForUpstream(env.cfg, upstream) == targetHost
	})
	require.True(t, restored, "cancel did not return the target upstream to active")
	requireS9StreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "evacuating the stream's target through gonka-hostctl")
	evacuationJournal := filepath.Join(env.stack.WorkDir, evacuationID+".json")
	evacuationCtx, interruptEvacuation := context.WithCancel(context.Background())
	interruptedEvacuation := make(chan error, 1)
	go func() {
		_, err := env.stack.RunHostctl(
			evacuationCtx,
			hostctl,
			"evacuate",
			hostctlArgs(routerContainer, targetContainer, targetHost, evacuationID, evacuationJournal)...,
		)
		interruptedEvacuation <- err
	}()
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost)
	interruptEvacuation()
	select {
	case err := <-interruptedEvacuation:
		require.Error(t, err, "interrupted evacuation unexpectedly completed")
	case <-time.After(15 * time.Second):
		t.Fatal("interrupted evacuation did not exit")
	}
	requireS9StreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "resuming evacuation from its durable hostctl phase")
	resumeCtx, cancelResume := context.WithTimeout(context.Background(), s10OperationTimeout)
	defer cancelResume()
	evacuationResult := make(chan error, 1)
	go func() {
		_, err := env.stack.RunHostctl(
			resumeCtx,
			hostctl,
			"evacuate",
			hostctlArgs(routerContainer, targetContainer, targetHost, evacuationID, evacuationJournal)...,
		)
		evacuationResult <- err
	}()
	requireNewRouterRequestsAvoidHost(t, client, env, escrowID, targetHost)

	harness.Step(t, "versiond remains alive while hostctl observes the established stream")
	observedInflight := harness.AssertEventually(t, s10ObservationTimeout, 250*time.Millisecond, func() bool {
		summary, err := harness.TryVersiondHealthSummary(env.stack, targetHost)
		return err == nil && summary.State == "serving" && summary.ProxyInflight > 0
	})
	require.True(t, observedInflight, "target versiond did not expose host inflight")
	running, err := env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.True(t, running, "target versiond exited before its accepted stream completed")
	requireS9StreamStillRunning(t, accepted, streamResult, "host evacuation stream")

	harness.Step(t, "the same escrow is recovered on the surviving host")
	requireSessionAvailableOnHost(t, env, escrowID, survivorHost)

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
	case <-time.After(s10OperationTimeout):
		t.Fatal("versiond did not exit after its host became idle")
	}
	running, err = env.stack.ServiceRunning(targetHost)
	require.NoError(t, err)
	require.False(t, running, "target versiond is still running after graceful stop")

	harness.Step(t, "replacement remains down until gonka-hostctl observes readiness")
	replacementJournal := filepath.Join(env.stack.WorkDir, replacementID+".json")
	replaceArgs := hostctlArgs(
		routerContainer,
		targetContainer,
		targetHost,
		replacementID,
		replacementJournal,
	)
	replaceArgs = append(replaceArgs, "--evacuation-journal", evacuationJournal)
	replaceCtx, cancelReplace := context.WithTimeout(context.Background(), s10OperationTimeout)
	output, err = env.stack.RunHostctl(replaceCtx, hostctl, "replace", replaceArgs...)
	cancelReplace()
	require.NoError(t, err, "replace versiond host: %s", output)
	replacementHealth, err := harness.TryVersiondHealthSummary(env.stack, targetHost)
	require.NoError(t, err)
	require.True(t, replacementHealth.Available)
	require.True(t, replacementHealth.Reconciled)
	require.False(t, replacementHealth.Progressing)
	require.False(t, replacementHealth.Degraded)
	requireSessionAvailableOnHost(t, env, escrowID, targetHost)

}

func requireSessionAvailableOnHost(
	t *testing.T,
	env s9RollingStack,
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
	available := harness.AssertEventually(t, s10ObservationTimeout, 250*time.Millisecond, func() bool {
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

func hostctlArgs(
	routerContainer string,
	versiondContainer string,
	host string,
	operationID string,
	journal string,
) []string {
	return []string{
		"--operation-id", operationID,
		"--journal", journal,
		"--router-ssh", "local",
		"--router-runtime", "docker",
		"--router-service", routerContainer,
		"--upstream", host,
		"--versiond-ssh", "local",
		"--versiond-runtime", "docker",
		"--versiond-service", versiondContainer,
		"--drain-timeout", s10DrainTimeout.String(),
		"--poll-interval", "250ms",
		"--kill-grace", s10KillGrace.String(),
		"--command-timeout", s10CommandTimeout.String(),
	}
}

func requireNewRouterRequestsAvoidHost(
	t *testing.T,
	client *http.Client,
	env s9RollingStack,
	escrowID string,
	targetHost string,
) {
	t.Helper()
	avoided := harness.AssertEventually(t, s10ObservationTimeout, 100*time.Millisecond, func() bool {
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

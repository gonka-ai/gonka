//go:build testenvci

package citest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cosrv "devshard/chainoracle/server"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

type versiondRollingTestStack struct {
	stack      *harness.Stack
	cfg        *config.File
	eps        harness.Endpoints
	oldVersion cosrv.Version
	newVersion cosrv.Version
	hosts      []string
}

// TestVersiondRollingUpdateSameVersionSHA verifies that a same-name sha
// change through the real oracle path keeps already accepted work alive while
// versiond swaps traffic to the newly downloaded devshardd archive.
func TestVersiondRollingUpdateSameVersionSHA(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	for targetHostIndex := 0; targetHostIndex < 2; targetHostIndex++ {
		t.Run(fmt.Sprintf("host_%d", targetHostIndex), func(t *testing.T) {
			env := bootVersiondRollingStack(t, "citest-versiond-rolling-*", true,
				func(stack *harness.Stack, _ *config.File) {
					// This suite pins traffic to one host by draining the other,
					// and the legacy owner cannot be drained — its backend has a
					// single server. Nothing here needs legacy pinning, so drop
					// it and the legacy backend becomes unreachable and is left
					// out of the drain entirely.
					harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", `""`)
				})
			client := harness.GatewayChatClient()

			delayMs := 750
			harness.PatchMockOpenAIFault(t, client, env.eps.MockOpenAIHTTP, mockopenai.FaultPatch{
				StreamChunkDelay: &delayMs,
			})

			targetHost := env.hosts[targetHostIndex]
			// Pin new work to the host under test so sticky hashing cannot hide
			// the drain on the other replica. Stopping it is how an operator takes
			// a host out of rotation, and it is what the router is built around.
			otherHost := env.hosts[1-targetHostIndex]
			harness.Step(t, "stopping %s so every new session lands on %s", otherHost, targetHost)
			env.stack.StopService(t, otherHost)
			harness.WaitRouterPoolState(t, env.stack, env.cfg,
				harness.VersionPoolBackend(env.cfg.Versiond.VersionName), otherHost,
				"", 60*time.Second)
			exerciseVersiondRollingFlip(t, &env, client, targetHost, env.oldVersion, env.newVersion, targetHost)
		})
	}
}

func exerciseVersiondRollingFlip(
	t *testing.T,
	env *versiondRollingTestStack,
	client *http.Client,
	overlapHost string,
	fromVersion cosrv.Version,
	toVersion cosrv.Version,
	label string,
) {
	t.Helper()

	harness.Step(t, "starting long stream on %s before %s rolling update", overlapHost, label)
	accepted, gatewayStreamResult := harness.StartGatewayChatCompletionStream(client, env.eps.GatewayHTTP, harness.TestenvAdminAPIKey,
		harness.ChatCompletionRequest{
			Model:     "test-model",
			MaxTokens: 64,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: label + " rolling update long stream",
			}},
		})
	requireVersiondStreamStillRunning(t, accepted, gatewayStreamResult, "gateway stream")

	probeCtx, stopProbe := context.WithCancel(context.Background())
	probeErr, err := startRouterContinuityProbe(
		probeCtx,
		client,
		env.eps.RouterHTTP+"/"+env.cfg.Versiond.VersionName+"/healthz",
	)
	require.NoError(t, err)
	defer stopProbe()

	harness.Step(t, "publishing new archive sha through mock-dapi /versions")
	harness.PatchTestenvVersions(t, client, env.eps.MockDapiHTTP, []cosrv.Version{toVersion})
	requireVersiondRollingOverlap(t, env.stack, []string{overlapHost}, env.cfg.Versiond.VersionName,
		fromVersion.SHA256, toVersion.SHA256)

	harness.Step(t, "new traffic succeeds after route swap")
	after := harness.PostGatewayChatCompletion(t, client, env.eps.GatewayHTTP, harness.TestenvAdminAPIKey,
		harness.ChatCompletionRequest{
			Model:     "test-model",
			MaxTokens: 64,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: label + " after rolling update",
			}},
		})
	harness.RequireMockOpenAIContent(t, after.Choices[0].Message.Content)

	harness.Step(t, "old in-flight stream completes cleanly")
	res := <-gatewayStreamResult
	require.NoError(t, res.Err)
	require.Equal(t, http.StatusOK, res.Status, "stream body: %s", res.Body)
	require.True(t, res.SawDone, "stream missing [DONE]")
	harness.RequireMockOpenAIContent(t, res.Content)
	stopProbe()
	select {
	case err := <-probeErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("continuity probe did not stop")
	}

	// Only the host that flipped: the other one is deliberately stopped, and a
	// stopped host has no health to report.
	requireNoOldDraining(t, env.stack, []string{overlapHost}, env.cfg.Versiond.VersionName, fromVersion.SHA256)
}

// TestVersiondRollingUpdateHybridFallback verifies that the same sha-flip
// does not overlap old and new children when devshardd storage is not Postgres.
func TestVersiondRollingUpdateHybridFallback(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	env := bootVersiondRollingStack(t, "citest-versiond-rolling-hybrid-*", false, func(stack *harness.Stack, _ *config.File) {
		harness.PatchVersiondStorageMode(t, stack.ComposePath, "hybrid")
	})
	client := harness.GatewayChatClient()

	harness.Step(t, "publishing new archive sha while storage mode is hybrid")
	harness.PatchTestenvVersions(t, client, env.eps.MockDapiHTTP, []cosrv.Version{env.newVersion})
	requireVersiondFallbackWithoutOverlap(t, env.stack, env.hosts, env.cfg.Versiond.VersionName,
		env.oldVersion.SHA256, env.newVersion.SHA256)
}

func bootVersiondRollingStack(t *testing.T, namePattern string, requireRouterChild bool, patchCompose func(*harness.Stack, *config.File)) versiondRollingTestStack {
	t.Helper()

	harness.Step(t, "starting stack with versiond download path (no local override)")
	stack := harness.NewStack(t, namePattern)
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

	versionName := cfg.Versiond.VersionName
	devsharddPath := filepath.Join(stack.TestenvDir, "..", "..", "build", "devshardd")
	binDir := filepath.Join(stack.WorkDir, "binaries")
	oldFile := "devshardd-old.zip"
	newFile := "devshardd-new.zip"
	oldSHA := harness.WriteDevsharddZip(t, devsharddPath, filepath.Join(binDir, oldFile), "rolling-old")
	newSHA := harness.WriteDevsharddZip(t, devsharddPath, filepath.Join(binDir, newFile), "rolling-new")
	oldVersion := cosrv.Version{
		Name:   versionName,
		Binary: harness.VersiondBinaryURL(cfg.MockDapi.HTTPPort, oldFile),
		SHA256: oldSHA,
	}
	newVersion := cosrv.Version{
		Name:   versionName,
		Binary: harness.VersiondBinaryURL(cfg.MockDapi.HTTPPort, newFile),
		SHA256: newSHA,
	}

	overrideKey := "VERSIOND_OVERRIDE_" + strings.ReplaceAll(versionName, ".", "_")
	harness.PatchComposeRemoveEnvKey(t, stack.ComposePath, overrideKey)
	harness.PatchComposeRemoveEnvKey(t, stack.ComposePath, "VERSIOND_FORCE")
	harness.PatchComposeInsertEnvAfter(t, stack.ComposePath, "MOCK_DAPI_BINARY_DIR",
		fmt.Sprintf(`MOCK_DAPI_VERSION_NAME: "%s"`, oldVersion.Name),
		fmt.Sprintf(`MOCK_DAPI_VERSION_BINARY: "%s"`, oldVersion.Binary),
		fmt.Sprintf(`MOCK_DAPI_VERSION_SHA256: "%s"`, oldVersion.SHA256),
	)
	if patchCompose != nil {
		patchCompose(stack, cfg)
	}

	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.GatewayChatClient()
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	if requireRouterChild {
		harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+versionName+"/healthz", 5*time.Minute,
			"initial devshardd health via router", stack)
	}

	hosts := []string{cfg.Hosts[0].ID, cfg.Hosts[1].ID}
	requireAllVersiondRunningSHA(t, stack, hosts, versionName, oldSHA)

	return versiondRollingTestStack{
		stack:      stack,
		cfg:        cfg,
		eps:        eps,
		oldVersion: oldVersion,
		newVersion: newVersion,
		hosts:      hosts,
	}
}

func requireVersiondStreamStillRunning(t *testing.T, accepted <-chan struct{}, result <-chan harness.GatewayStreamResult, label string) {
	t.Helper()
	select {
	case <-accepted:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s was not accepted before rolling update", label)
	}
	select {
	case res := <-result:
		require.NoError(t, res.Err)
		require.Equal(t, http.StatusOK, res.Status, "%s ended before rolling update: %s", label, res.Body)
		t.Fatalf("%s ended before rolling update", label)
	default:
	}
}

func requireAllVersiondRunningSHA(t *testing.T, stack *harness.Stack, hosts []string, versionName, sha string) {
	t.Helper()
	ok := harness.AssertEventually(t, 2*time.Minute, time.Second, func() bool {
		for _, host := range hosts {
			entries, err := harness.TryVersiondHealth(stack, host)
			if err != nil || !harness.HasVersiondHealthEntry(entries, versionName, "running", sha) {
				return false
			}
		}
		return true
	})
	require.True(t, ok, "versiond hosts did not converge to running sha %s", sha)
}

func requireVersiondRollingOverlap(t *testing.T, stack *harness.Stack, hosts []string, versionName, oldSHA, newSHA string) {
	t.Helper()
	sawOverlap := make(map[string]bool, len(hosts))
	ok := harness.AssertEventually(t, 2*time.Minute, 100*time.Millisecond, func() bool {
		allNewRunning := true
		allSawOverlap := true
		for _, host := range hosts {
			entries, err := harness.TryVersiondHealth(stack, host)
			if err != nil {
				allNewRunning = false
				continue
			}
			if harness.HasVersiondHealthEntry(entries, versionName, "running", newSHA) &&
				harness.HasVersiondHealthEntry(entries, versionName, "draining", oldSHA) {
				sawOverlap[host] = true
			}
			if !harness.HasVersiondHealthEntry(entries, versionName, "running", newSHA) {
				allNewRunning = false
			}
			if !sawOverlap[host] {
				allSawOverlap = false
			}
		}
		return allSawOverlap && allNewRunning
	})
	require.True(t, ok, "did not observe per-host running new sha %s with draining old sha %s", newSHA, oldSHA)
}

func requireVersiondFallbackWithoutOverlap(t *testing.T, stack *harness.Stack, hosts []string, versionName, oldSHA, newSHA string) {
	t.Helper()
	ok := harness.AssertEventually(t, 2*time.Minute, 100*time.Millisecond, func() bool {
		allNewRunning := true
		for _, host := range hosts {
			entries, err := harness.TryVersiondHealth(stack, host)
			if err != nil {
				allNewRunning = false
				continue
			}
			require.False(t, harness.HasVersiondHealthEntry(entries, versionName, "draining", oldSHA),
				"hybrid fallback unexpectedly drained old sha %s on host %s", oldSHA, host)
			if !harness.HasVersiondHealthEntry(entries, versionName, "running", newSHA) {
				allNewRunning = false
			}
		}
		return allNewRunning
	})
	require.True(t, ok, "versiond hosts did not converge to fallback running sha %s", newSHA)
}

func requireNoOldDraining(t *testing.T, stack *harness.Stack, hosts []string, versionName, oldSHA string) {
	t.Helper()
	ok := harness.AssertEventually(t, 2*time.Minute, time.Second, func() bool {
		for _, host := range hosts {
			entries, err := harness.TryVersiondHealth(stack, host)
			if err != nil || harness.HasVersiondHealthEntry(entries, versionName, "draining", oldSHA) {
				return false
			}
		}
		return true
	})
	require.True(t, ok, "old sha %s still draining", oldSHA)
}

func startRouterContinuityProbe(
	ctx context.Context,
	client *http.Client,
	url string,
) (<-chan error, error) {
	// Complete one request synchronously. The caller can start its mutation only
	// after the probe has demonstrably covered the pre-transition target.
	if err := probeRouterContinuity(client, url); err != nil {
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
				if err := probeRouterContinuity(client, url); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()
	return errCh, nil
}

func probeRouterContinuity(client *http.Client, url string) error {
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

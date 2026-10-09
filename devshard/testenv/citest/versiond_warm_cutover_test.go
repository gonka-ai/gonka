//go:build testenvci

package citest

import (
	"testing"
	"time"

	cosrv "devshard/chainoracle/server"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestVersiondWarmCutoverBoot pins the boot half of the warm-cutover
// contract (companion ready-on-boot-warm-cutover flow): a v5 devshardd child
// comes up serving on its public listener while its recovery backlog may still
// be draining, and versiond admits it. The status-vs-body split is what makes a
// solo restart publish on status code alone; this test pins that the public
// health path answers 200 end to end with VERSIOND_RECOVERY_TIMEOUT configured,
// so a future change that accidentally gated the public listener on
// recovery_complete would fail here.
//
// The recovery_complete body field itself is asserted at the unit level
// (devshard/cmd/devshardd/lifecycle_test.go) and the versiond wait logic in
// versioned/internal/process/manager_recovery_wait_test.go; the admin /ready
// listener is loopback inside the versiond container on a dynamic port and is
// not reachable from the test host, so this test observes the externally
// visible contract: public /healthz 200 + versiond reporting the child running.
func TestVersiondWarmCutoverBoot(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	env := bootVersiondRollingStack(t, "citest-versiond-warm-cutover-boot-*", true,
		func(stack *harness.Stack, _ *config.File) {
			// VERSIOND_RECOVERY_TIMEOUT is new in v5; gencompose does not emit it,
			// so insert it next to the other VERSIOND_* knobs in the versiond env.
			harness.PatchComposeInsertEnvAfter(t, stack.ComposePath, "VERSIOND_POLL_INTERVAL",
				`VERSIOND_RECOVERY_TIMEOUT: "30s"`)
			harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", `""`)
		})
	client := harness.GatewayChatClient()

	harness.Step(t, "devshardd public health is 200 via the router while recovery may be draining")
	versionHealth := env.eps.RouterHTTP + "/" + env.cfg.Versiond.VersionName + "/healthz"
	harness.WaitGETOK(t, client, versionHealth, 5*time.Minute,
		"devshardd public /healthz after warm-cutover boot", env.stack)

	harness.Step(t, "both versiond hosts report the child running with the booted sha")
	requireAllVersiondRunningSHA(t, env.stack, env.hosts, env.cfg.Versiond.VersionName, env.oldVersion.SHA256)

	// A chat round-trip proves the admitted child actually serves inference,
	// not just health — i.e. the public listener is wired through to the SM.
	chat := harness.PostGatewayChatCompletion(t, client, env.eps.GatewayHTTP, harness.TestenvAdminAPIKey,
		harness.ChatCompletionRequest{
			Model:     "test-model",
			MaxTokens: 32,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: "warm-cutover boot smoke",
			}},
		})
	harness.RequireMockOpenAIContent(t, chat.Choices[0].Message.Content)
}

// TestVersiondWarmCutoverOverlapWaitsThenServes pins the swap half of the
// warm-cutover contract: in the blue/green overlap branch, versiond waits for
// the new child's recovery_complete before publishing it. With the testenv's
// empty journal the wait returns in milliseconds, so this test cannot observe
// the wait duration. Instead of asserting the timing-sensitive
// running(new)+draining(old) overlap pair (which needs an artificial in-flight
// stream to keep the old child draining long enough to sample), it asserts the
// load-bearing invariant directly: the new sha reaches `running` on the target
// host, which only happens after waitForChildRecoveryComplete returns and
// downloadAndSwap publishes the new child. A broken or deadlocked warm wait
// would abort the swap (ErrRecoveryTimeout) and the old child would keep
// serving — the new sha would never appear as `running`, failing the 3m poll
// (well past the 30s VERSIOND_RECOVERY_TIMEOUT). New traffic then serves on the
// new child and the old sha retires. This is the end-to-end companion to the
// unit tests in manager_recovery_wait_test.go, which cover the bail-outs.
func TestVersiondWarmCutoverOverlapWaitsThenServes(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	env := bootVersiondRollingStack(t, "citest-versiond-warm-cutover-overlap-*", true,
		func(stack *harness.Stack, _ *config.File) {
			harness.PatchComposeInsertEnvAfter(t, stack.ComposePath, "VERSIOND_POLL_INTERVAL",
				`VERSIOND_RECOVERY_TIMEOUT: "30s"`)
			harness.PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", `""`)
		})
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, env.stack, "versiond-0", "versiond-1", "versiond-router")
		}
	})

	// Pin every new session to one host so the overlap is observable there.
	targetHost := env.hosts[0]
	otherHost := env.hosts[1]
	harness.Step(t, "stopping %s so the sha flip overlaps only on %s", otherHost, targetHost)
	env.stack.StopService(t, otherHost)
	probeURL := harness.RouterSessionURL(env.eps.RouterHTTP,
		env.cfg.Versiond.VersionName, "warm-pin", "/healthz")
	pinned := harness.AssertEventually(t, 60*time.Second, 250*time.Millisecond, func() bool {
		upstream, err := harness.GetResponseHeader(client, probeURL, harness.StickyUpstreamHeader)
		return err == nil && harness.HostIDForUpstream(env.cfg, upstream) == targetHost
	})
	require.True(t, pinned, "router did not withdraw stopped host %s", otherHost)

	harness.Step(t, "publishing new archive sha through mock-dapi /versions")
	harness.PatchTestenvVersions(t, client, env.eps.MockDapiHTTP, []cosrv.Version{env.newVersion})

	// Load-bearing warm-cutover check: the new sha reaches `running` on the
	// target host. This only happens after waitForChildRecoveryComplete returns
	// and downloadAndSwap publishes the new child. A broken or deadlocked warm
	// wait would abort the swap (ErrRecoveryTimeout) and the old child would
	// keep serving — the new sha would never appear as `running`. The 3m window
	// is well past the 30s VERSIOND_RECOVERY_TIMEOUT, so a timeout-aborted swap
	// fails this assertion rather than the test hanging on the poll.
	versionName := env.cfg.Versiond.VersionName
	newSHA := env.newVersion.SHA256
	oldSHA := env.oldVersion.SHA256
	swapped := harness.AssertEventually(t, 3*time.Minute, 200*time.Millisecond, func() bool {
		entries, err := harness.TryVersiondHealth(env.stack, targetHost)
		if err != nil {
			return false
		}
		return harness.HasVersiondHealthEntry(entries, versionName, "running", newSHA)
	})
	require.True(t, swapped,
		"new sha %s never reached running on %s (warm wait may have aborted the swap); "+
			"see versiond-0 logs for \"warm cutover\" / \"warm-cutover wait timed out\"",
		newSHA, targetHost)

	harness.Step(t, "new traffic succeeds after the warm-cutover swap")
	after := harness.PostGatewayChatCompletion(t, client, env.eps.GatewayHTTP, harness.TestenvAdminAPIKey,
		harness.ChatCompletionRequest{
			Model:     "test-model",
			MaxTokens: 64,
			Messages: []harness.ChatMessage{{
				Role:    "user",
				Content: "warm-cutover after swap",
			}},
		})
	harness.RequireMockOpenAIContent(t, after.Choices[0].Message.Content)

	harness.Step(t, "old sha is fully retired (no lingering draining child)")
	requireNoOldDraining(t, env.stack, []string{targetHost}, versionName, oldSHA)
}

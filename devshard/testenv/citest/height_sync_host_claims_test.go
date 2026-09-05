//go:build testenvci

package citest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const hostClaimsReconcileWarn = "untrusted peer tip disagrees with oracle at reconciled height"

func bootHeightSyncHostClaimStack(t *testing.T, prefix string, delta int64, fabricate bool) (*harness.Stack, *config.File, harness.Endpoints, string) {
	t.Helper()
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	stack, cfg, eps := harness.BootHeightSyncSoloOracleOverlayStack(t, prefix, delta, fabricate)
	solo := harness.FirstSoloHostID(t, cfg)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", solo)
		}
	})
	return stack, cfg, eps, solo
}

func waitHostClaimOverlayArmed(t *testing.T, stack *harness.Stack, solo string) {
	t.Helper()
	stack.WaitComposeLogsContain(t, 2*time.Minute, "height sync testenv oracle overlay", solo)
}

func waitHostClaimChatReady(t *testing.T, stack *harness.Stack, eps harness.Endpoints) {
	t.Helper()
	client := harness.GatewayChatClient()
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
}

func composeLogs(t *testing.T, stack *harness.Stack, services ...string) string {
	t.Helper()
	out, err := stack.ComposeLogsTail(2000, services...)
	require.NoError(t, err)
	return out
}

func composeLogsContains(stack *harness.Stack, needle string, services ...string) bool {
	out, err := stack.ComposeLogsTail(2000, services...)
	return err == nil && strings.Contains(out, needle)
}

// TestContainerE2E_HeightSync_HostLowerHeightAutoAligns is scenario A: the solo
// identity's Latest() is patched 20 blocks behind. Chat must keep returning 200;
// operators see a negative delta and a non-zero height_spread / host_height_lag.
func TestContainerE2E_HeightSync_HostLowerHeightAutoAligns(t *testing.T) {
	stack, cfg, eps, solo := bootHeightSyncHostClaimStack(t, "citest-hs-claim-a-*", -20, false)
	waitHostClaimOverlayArmed(t, stack, solo)
	waitHostClaimChatReady(t, stack, eps)

	for i := 0; i < 4; i++ {
		postHeightSyncChat(t, cfg, eps, fmt.Sprintf("citest height-sync host lower height %d", i))
	}

	client := harness.GatewayChatClient()
	body := harness.WaitMetricsPredicate(t, client, eps.GatewayHTTP+"/metrics", 2*time.Minute, func(b string) bool {
		spread, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_height_spread", nil)
		return ok && spread >= 15
	})
	spread, _ := harness.MetricLineValue(body, "devshard_gateway_heightsync_height_spread", nil)
	require.GreaterOrEqual(t, spread, 15.0, "solo lag of 20 must show up as height_spread")

	logs := composeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", solo)
	require.True(t, strings.Contains(logs, "delta=-"),
		"operators must see a negative delta for the lagging host; logs:\n%s", logs)
	require.Contains(t, logs, "trust_level=peer_aligned")
}

// TestContainerE2E_HeightSync_HostFutureHeightBeyondD is scenario B: the solo
// identity claims ~10 blocks ahead with a fabricated hash (|Δ| > D=2). Chat
// still 200; trust_level=untrusted_peer; L5a marks the admission once a
// heartbeat/ack with that height is bound to an envelope.
func TestContainerE2E_HeightSync_HostFutureHeightBeyondD(t *testing.T) {
	stack, cfg, eps, solo := bootHeightSyncHostClaimStack(t, "citest-hs-claim-b-*", 10, true)
	waitHostClaimOverlayArmed(t, stack, solo)
	waitHostClaimChatReady(t, stack, eps)

	for i := 0; i < 4; i++ {
		postHeightSyncChat(t, cfg, eps, fmt.Sprintf("citest height-sync host future height %d", i))
	}

	client := harness.GatewayChatClient()
	harness.WaitMetricsPredicate(t, client, eps.GatewayHTTP+"/metrics", 2*time.Minute, func(b string) bool {
		spread, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_height_spread", nil)
		return ok && spread >= 8
	})

	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		return composeLogsContains(stack, "trust_level=untrusted_peer", "devshardctl", "versiond-0", "versiond-1", solo)
	})
	require.True(t, ok, "future claim |Δ|>D must log trust_level=untrusted_peer")

	stack.WaitComposeLogsContain(t, 2*time.Minute, "heartbeat_opened", "devshardctl")
	ok = harness.AssertEventually(t, 2*time.Minute, 3*time.Second, func() bool {
		postHeightSyncChat(t, cfg, eps, "citest height-sync host future height l5a probe")
		return composeLogsContains(stack, "l5a_admission", "versiond-0", "versiond-1", solo)
	})
	require.True(t, ok, "L5a MARK(l5a_admission) must fire on an honest host that admitted |Δ|>D")
}

// TestContainerE2E_HeightSync_HostFabricatedHashInsideD is scenario C: the solo
// identity claims H+1 (inside D) with a fabricated hash. Chat keeps working.
// When an honest follower's oracle later reaches that held height, it warns.
func TestContainerE2E_HeightSync_HostFabricatedHashInsideD(t *testing.T) {
	stack, cfg, eps, solo := bootHeightSyncHostClaimStack(t, "citest-hs-claim-c-*", 1, true)
	waitHostClaimOverlayArmed(t, stack, solo)
	waitHostClaimChatReady(t, stack, eps)

	postHeightSyncChat(t, cfg, eps, "citest height-sync fabricated hash seed a")
	postHeightSyncChat(t, cfg, eps, "citest height-sync fabricated hash seed b")
	postHeightSyncChat(t, cfg, eps, "citest height-sync fabricated hash seed c")

	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		return composeLogsContains(stack, "trust_level=untrusted_peer", "devshardctl", "versiond-0", "versiond-1", solo)
	})
	require.True(t, ok, "H+1 fabricated claim must log untrusted_peer before reconcile")

	// Pending is the last carried H+1. Reconcile warns only when local Latest()
	// equals that height (not later). Keep chatting across both slots so the
	// HA host handles a request on the tick that catches the held tip.
	n := 0
	ok = harness.AssertEventually(t, 45*time.Second, time.Second, func() bool {
		n++
		postHeightSyncChat(t, cfg, eps, fmt.Sprintf("citest height-sync fabricated hash reconcile %d", n))
		return composeLogsContains(stack, hostClaimsReconcileWarn, "versiond-0", "versiond-1")
	})
	require.True(t, ok, "honest host must warn when its oracle reaches the fabricated height")
}

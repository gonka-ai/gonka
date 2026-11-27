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

const (
	hostClaimsReconcileWarn  = "untrusted peer tip disagrees with oracle at reconciled height"
	lowerClaimMinimumSpread  = 15
	futureClaimMinimumSpread = 8
	heightSyncWait           = 2 * time.Minute
	busyDischargeSlack       = 20 * time.Second
)

func bootHeightSyncHostClaimStack(t *testing.T, prefix string, delta int64, fabricate bool) (*harness.Stack, *config.File, harness.Endpoints, string) {
	t.Helper()
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	stack, cfg, eps := harness.BootHeightSyncSoloOracleOverlayStack(t, prefix, delta, fabricate)
	solo := harness.FirstSoloHostID(t, cfg)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1", solo)
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
	out, err := stack.ComposeLogsAll(services...)
	require.NoError(t, err)
	return out
}

func composeLogsContains(stack *harness.Stack, needle string, services ...string) bool {
	ok, err := stack.ComposeLogsContain(needle, services...)
	return err == nil && ok
}

func composeLogsHaveField(stack *harness.Stack, field harness.LogField, services ...string) bool {
	out, err := stack.ComposeLogsAll(services...)
	return err == nil && harness.LogsContainFields(out, field)
}

func dumpHeightSyncLogs(t *testing.T, stack *harness.Stack, services ...string) {
	t.Helper()
	out, err := stack.ComposeLogsAll(services...)
	if err != nil {
		t.Logf("citest: compose logs: %v", err)
		return
	}
	var matched []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "heightsync") {
			matched = append(matched, line)
		}
	}
	t.Logf("citest: heightsync log lines (%d):\n%s", len(matched), strings.Join(matched, "\n"))
}

// TestContainerE2E_HeightSync_HostLowerHeightAutoAligns is scenario A: the solo
// identity's Latest() is patched 20 blocks behind. Chat must keep returning 200;
// operators see a negative delta and a height spread on the gateway's height-sync view.
func TestContainerE2E_HeightSync_HostLowerHeightAutoAligns(t *testing.T) {
	stack, cfg, eps, solo := bootHeightSyncHostClaimStack(t, "citest-hs-claim-a-*", -20, false)
	waitHostClaimOverlayArmed(t, stack, solo)
	waitHostClaimChatReady(t, stack, eps)

	for i := 0; i < 4; i++ {
		postHeightSyncChat(t, cfg, eps, fmt.Sprintf("citest height-sync host lower height %d", i))
	}

	view := waitHeightSpread(t, eps, lowerClaimMinimumSpread)
	spread, _ := view.HeightSpread()
	require.GreaterOrEqual(t, spread, uint64(lowerClaimMinimumSpread), "solo lag of 20 must show up as the height spread")

	logs := composeLogs(t, stack, "gateway", "versiond-0", "versiond-1", solo)
	require.True(t, harness.LogsContainFields(logs, harness.LogNumberPrefixField("delta", "-")),
		"operators must see a negative delta for the lagging host; logs:\n%s", logs)
	require.True(t, harness.LogsContainFields(logs, harness.LogStringField("trust_level", "peer_aligned")))
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

	escrowID := waitHeightSpread(t, eps, futureClaimMinimumSpread).DevshardID

	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		return composeLogsHaveField(stack, harness.LogStringField("trust_level", "untrusted_peer"), "gateway", "versiond-0", "versiond-1", solo)
	})
	require.True(t, ok, "future claim |Δ|>D must log trust_level=untrusted_peer")

	waitHeartbeatOpened(t, eps, escrowID)
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
		return composeLogsHaveField(stack, harness.LogStringField("trust_level", "untrusted_peer"), "gateway", "versiond-0", "versiond-1", solo)
	})
	require.True(t, ok, "H+1 fabricated claim must log untrusted_peer before reconcile")

	// Courier delay plus 1s blocks usually deliver the fabricated pair after
	// honest Latest() has already reached or passed H. The host compares the
	// claim to Latest() or Oracle.At(H); keep chatting so an HA replica sees it.
	n := 0
	ok = harness.AssertEventually(t, 45*time.Second, time.Second, func() bool {
		if composeLogsContains(stack, hostClaimsReconcileWarn, "versiond-0", "versiond-1") {
			return true
		}
		n++
		postHeightSyncChat(t, cfg, eps, fmt.Sprintf("citest height-sync fabricated hash reconcile %d", n))
		return composeLogsContains(stack, hostClaimsReconcileWarn, "versiond-0", "versiond-1")
	})
	if !ok {
		dumpHeightSyncLogs(t, stack, "gateway", "versiond-0", "versiond-1", solo)
	}
	require.True(t, ok, "honest host must warn when its oracle reaches the fabricated height")
}

func waitHeightSpread(t *testing.T, eps harness.Endpoints, minimumSpread uint64) harness.HeightSyncView {
	t.Helper()
	escrowID := harness.GetGatewayEscrowID(t, nil, eps.GatewayHTTP)
	return harness.WaitHeightSyncView(t, nil, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		spread, claimed := view.HeightSpread()
		return claimed && spread >= minimumSpread
	})
}

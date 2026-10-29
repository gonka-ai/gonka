//go:build testenvci

package citest

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/hostevents"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockdapi"

	"github.com/stretchr/testify/require"
)

const (
	blockFeedTimeout     = 2 * time.Minute
	blockFeedGrantID     = uint64(42)
	blockFeedGrantee     = "gonka1blockfeedwarm"
	blockFeedBadHeight   = int64(9_000_001)
	followerEnabledMsg   = "height sync chain follower enabled"
	getHostEventsMsg     = "hostevents: GetHostEvents"
	needsResetMsg        = "hostevents: needs_reset"
	warmKeyMsg           = "hostevents: warm-key"
	headerTimeAppliedMsg = "header_time: applied"
	dapiHeaderRejected   = "dapi header rejected"
)

func bootBlockFeed(t *testing.T, prefix string) (*harness.Stack, *config.File, harness.Endpoints, *http.Client) {
	t.Helper()
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, prefix)
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-dapi", "mock-chain")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	stack.RequireServicesRunning(t, "devshardctl", cfg.Hosts[0].ID, cfg.Hosts[1].ID, "mock-dapi", "mock-chain")
	return stack, cfg, eps, harness.HTTPClient()
}

func waitGatewayDAPITip(t *testing.T, client *http.Client, eps harness.Endpoints, minHeight uint64) hostevents.HeaderTimeView {
	t.Helper()
	return harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.BlockFeedLive && v.HighWater >= minHeight && v.TipOrigin == "dapi" && v.TipVerified
	})
}

func waitHeaderTimeApplied(t *testing.T, stack *harness.Stack, service string, height uint64) {
	t.Helper()
	want := strconv.FormatUint(height, 10)
	ok := harness.AssertEventually(t, blockFeedTimeout, 2*time.Second, func() bool {
		logs, err := stack.ComposeLogsAll(service)
		if err != nil {
			return false
		}
		return harness.LogsContainFields(logs,
			harness.LogStringField("msg", headerTimeAppliedMsg),
			harness.LogNumberField("height", want),
			harness.LogStringField("origin", "dapi"),
			harness.LogBoolField("verified", true),
		)
	})
	require.True(t, ok, "%s missing %s height=%s origin=dapi verified=true", service, headerTimeAppliedMsg, want)
}

func logsAfterLast(logs, marker string) string {
	idx := strings.LastIndex(logs, marker)
	if idx < 0 {
		return logs
	}
	return logs[idx:]
}

func countContaining(logs, needle string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

func countNonZeroAfterHeight(logs string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, getHostEventsMsg) {
			continue
		}
		if harness.LogsContainFields(line, harness.LogNumberField("after_height", "0")) {
			continue
		}
		if strings.Contains(line, "after_height") {
			n++
		}
	}
	return n
}

func waitLogFields(t *testing.T, stack *harness.Stack, service string, fields ...harness.LogField) {
	t.Helper()
	ok := harness.AssertEventually(t, blockFeedTimeout, 2*time.Second, func() bool {
		logs, err := stack.ComposeLogsAll(service)
		return err == nil && harness.LogsContainFields(logs, fields...)
	})
	require.True(t, ok, "%s missing log fields %v", service, fields)
}

// TestBlockFeed_LiveHeights advances mock-chain and requires the gateway map
// and both versiond hosts to name that tip from dapi, verified.
func TestBlockFeed_LiveHeights(t *testing.T) {
	stack, _, eps, client := bootBlockFeed(t, "citest-block-feed-live-*")

	harness.Step(t, "wait for a live dapi tip on the gateway")
	harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.BlockFeedLive && v.HighWater > 0
	})

	harness.Step(t, "advance mock-chain")
	height := uint64(harness.WaitMockChainAdvance(t, client, eps, 20*time.Second))
	view := waitGatewayDAPITip(t, client, eps, height)
	require.GreaterOrEqual(t, view.HighWater, height)
	require.Equal(t, "dapi", view.TipOrigin)
	require.True(t, view.TipVerified)

	tip := view.HighWater
	harness.Step(t, "versiond hosts log header_time: applied for height %d", tip)
	waitHeaderTimeApplied(t, stack, "versiond-0", tip)
	waitHeaderTimeApplied(t, stack, "versiond-1", tip)
}

// TestBlockFeed_CatchupAfterRestart restarts the gateway and requires one
// non-zero after_height with no needs_reset.
func TestBlockFeed_CatchupAfterRestart(t *testing.T) {
	stack, cfg, eps, client := bootBlockFeed(t, "citest-block-feed-catchup-*")

	harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.HighWater > 0
	})
	height := uint64(harness.WaitMockChainAdvance(t, client, eps, 20*time.Second))
	before := waitGatewayDAPITip(t, client, eps, height)

	beforeLogs := composeLogs(t, stack, "devshardctl")
	nFollowers := countContaining(beforeLogs, followerEnabledMsg)

	harness.Step(t, "restart devshardctl from high_water %d", before.HighWater)
	harness.RestartService(t, stack, "devshardctl")
	eps = stack.Endpoints(t, cfg)
	harness.WaitGETOK(t, client, eps.GatewayHTTP+"/v1/status", 2*time.Minute, "gateway after restart", stack)

	after := harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.HighWater >= before.HighWater
	})
	require.GreaterOrEqual(t, after.HighWater, before.HighWater)

	var tail string
	ok := harness.AssertEventually(t, blockFeedTimeout, 2*time.Second, func() bool {
		logs := composeLogs(t, stack, "devshardctl")
		if countContaining(logs, followerEnabledMsg) < nFollowers+1 {
			return false
		}
		tail = logsAfterLast(logs, followerEnabledMsg)
		return countNonZeroAfterHeight(tail) >= 1
	})
	require.True(t, ok, "restarted gateway did not log a non-zero after_height")
	require.Equal(t, 1, countNonZeroAfterHeight(tail), "expected one non-zero after_height after restart\n%s", tail)
	require.Zero(t, countContaining(tail, needsResetMsg), "restarted gateway logged needs_reset\n%s", tail)
}

// TestBlockFeed_StallUsesComet fills the stalled height from Comet, then the
// next height from dapi, and applies a warm-key grant posted during the stall.
func TestBlockFeed_StallUsesComet(t *testing.T) {
	stack, _, eps, client := bootBlockFeed(t, "citest-block-feed-stall-*")
	mockDapi := harness.MockDAPIFromEndpoints(eps)

	harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.BlockFeedLive && v.HighWater > 0
	})

	harness.Step(t, "stall GetHostEvents")
	harness.StallHostEvents(t, client, mockDapi.HTTP, true)
	t.Cleanup(func() { harness.StallHostEvents(t, client, mockDapi.HTTP, false) })
	harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return !v.BlockFeedLive
	})

	stalledHeight := uint64(harness.WaitMockChainAdvance(t, client, eps, 20*time.Second))
	stalled := harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.HighWater >= stalledHeight && v.TipOrigin == "comet"
	})
	require.Equal(t, "comet", stalled.TipOrigin)
	require.GreaterOrEqual(t, stalled.HighWater, stalledHeight)

	harness.Step(t, "post warm-key grant while stalled")
	harness.PostHostEventWarmKey(t, client, mockDapi.HTTP, mockdapi.WarmKeyRequest{
		Granter: "cold",
		Grantee: blockFeedGrantee,
		GrantID: blockFeedGrantID,
		Height:  int64(stalledHeight),
	})

	harness.Step(t, "clear stall and advance one block")
	harness.StallHostEvents(t, client, mockDapi.HTTP, false)
	nextHeight := uint64(harness.WaitMockChainAdvance(t, client, eps, 20*time.Second))
	live := waitGatewayDAPITip(t, client, eps, nextHeight)
	require.Equal(t, "dapi", live.TipOrigin)
	require.GreaterOrEqual(t, live.HighWater, nextHeight)

	waitLogFields(t, stack, "devshardctl",
		harness.LogStringField("msg", warmKeyMsg),
		harness.LogStringField("grantee", blockFeedGrantee),
		harness.LogNumberField("grant_id", strconv.FormatUint(blockFeedGrantID, 10)),
	)
}

// TestBlockFeed_ResetOnce bumps the mock generation and requires one needs_reset
// then a follow-up poll with after_height=0 that catches the new tip.
func TestBlockFeed_ResetOnce(t *testing.T) {
	stack, _, eps, client := bootBlockFeed(t, "citest-block-feed-reset-*")
	mockDapi := harness.MockDAPIFromEndpoints(eps)

	before := waitGatewayDAPITip(t, client, eps, 1)
	beforeLogs := composeLogs(t, stack, "devshardctl")
	nReset := countContaining(beforeLogs, needsResetMsg)

	harness.Step(t, "bump host-event generation")
	harness.BumpHostEventGeneration(t, client, mockDapi.HTTP)
	ok := harness.AssertEventually(t, blockFeedTimeout, 2*time.Second, func() bool {
		return countContaining(composeLogs(t, stack, "devshardctl"), needsResetMsg) == nReset+1
	})
	require.True(t, ok, "gateway did not log exactly one needs_reset after the bump")

	newHeight := uint64(harness.WaitMockChainAdvance(t, client, eps, 20*time.Second))
	after := harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.HighWater >= newHeight && v.HighWater >= before.HighWater
	})
	require.GreaterOrEqual(t, after.HighWater, newHeight)

	logs := composeLogs(t, stack, "devshardctl")
	require.Equal(t, nReset+1, countContaining(logs, needsResetMsg), "a second needs_reset fails the test\n%s", logs)
	require.True(t, harness.LogsContainFields(logs,
		harness.LogStringField("msg", getHostEventsMsg),
		harness.LogNumberField("after_height", "0"),
	), "expected a follow-up GetHostEvents with after_height=0\n%s", logs)
}

// TestBlockFeed_WarmKeyFromDapi applies a grant and a revoke from mock-dapi on
// the gateway and both versiond hosts, without a chain warm-key read.
func TestBlockFeed_WarmKeyFromDapi(t *testing.T) {
	stack, _, eps, client := bootBlockFeed(t, "citest-block-feed-warm-*")
	mockDapi := harness.MockDAPIFromEndpoints(eps)

	harness.WaitGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, blockFeedTimeout, func(v hostevents.HeaderTimeView) bool {
		return v.BlockFeedLive
	})

	harness.Step(t, "post warm-key grant")
	harness.PostHostEventWarmKey(t, client, mockDapi.HTTP, mockdapi.WarmKeyRequest{
		Granter: "cold",
		Grantee: blockFeedGrantee,
		GrantID: blockFeedGrantID,
		Height:  8,
	})
	for _, svc := range []string{"devshardctl", "versiond-0", "versiond-1"} {
		waitLogFields(t, stack, svc,
			harness.LogStringField("msg", warmKeyMsg),
			harness.LogStringField("grantee", blockFeedGrantee),
			harness.LogNumberField("grant_id", strconv.FormatUint(blockFeedGrantID, 10)),
		)
	}

	harness.Step(t, "post warm-key revoke")
	harness.PostHostEventWarmKey(t, client, mockDapi.HTTP, mockdapi.WarmKeyRequest{
		Granter: "cold",
		Grantee: blockFeedGrantee,
		GrantID: blockFeedGrantID,
		Height:  9,
		End:     "WARM_KEY_LOG_END_REVOKED",
	})
	for _, svc := range []string{"devshardctl", "versiond-0", "versiond-1"} {
		waitLogFields(t, stack, svc,
			harness.LogStringField("msg", warmKeyMsg),
			harness.LogStringField("grantee", blockFeedGrantee),
			harness.LogStringField("end", "WARM_KEY_LOG_END_REVOKED"),
		)
	}

	logs := composeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
	require.NotContains(t, logs, "WarmKeyLog ", "grant/revoke must not read the chain warm-key log")
	require.NotContains(t, logs, "warm-key log of")
}

// TestBlockFeed_GatewayDropsBadHeader leaves the gateway map off a BLOCK whose
// header does not hash to hash, and lets devshardd store that height.
func TestBlockFeed_GatewayDropsBadHeader(t *testing.T) {
	stack, _, eps, client := bootBlockFeed(t, "citest-block-feed-bad-*")
	mockDapi := harness.MockDAPIFromEndpoints(eps)

	before := waitGatewayDAPITip(t, client, eps, 1)

	harness.Step(t, "append BLOCK %d with a header that does not hash to hash", blockFeedBadHeight)
	harness.PostHostEventBlock(t, client, mockDapi.HTTP, mockdapi.BlockAppendRequest{
		Height:           blockFeedBadHeight,
		Hash:             []byte{0xde, 0xad},
		HeaderTimeUnixMs: 1,
		Header:           []byte("not-a-protobuf-header"),
	})

	ok := harness.AssertEventually(t, blockFeedTimeout, 2*time.Second, func() bool {
		logs, err := stack.ComposeLogsAll("devshardctl")
		return err == nil && strings.Contains(logs, dapiHeaderRejected)
	})
	require.True(t, ok, "gateway log missing %q", dapiHeaderRejected)

	view := harness.GetGatewayHeaderTime(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	require.NotEqual(t, uint64(blockFeedBadHeight), view.HighWater, "gateway high_water moved to the rejected height")
	require.Less(t, view.HighWater, uint64(blockFeedBadHeight))
	require.GreaterOrEqual(t, view.HighWater, before.HighWater)

	waitHeaderTimeApplied(t, stack, "versiond-0", uint64(blockFeedBadHeight))
	waitHeaderTimeApplied(t, stack, "versiond-1", uint64(blockFeedBadHeight))
}

//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestValidationLeaseRaceCore drives chat load under HA + 100% validation_rate,
// monitors Postgres leases in parallel, and PASS/FAILs on uniqueness and on
// every validator replaying and passing the mock replies (manual plan §§4–6).
func TestValidationLeaseRaceCore(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootValidationLeaseRaceStack(t, "citest-validation-lease-race-core-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "devshardctl", "mock-openai-0", "devshard-postgres")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health", stack)

	dapi := harness.MockDAPIFromEndpoints(eps)
	harness.Step(t, "set validation_rate=10000 before escrow create")
	harness.SetValidationRate100(t, client, dapi.HTTP)

	harness.Step(t, "seed escrow + warm both HA replicas")
	model := config.PrimaryModelID(cfg)
	seed := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest validation lease race seed"},
		},
		MaxTokens: 16,
	}
	harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, seed)
	snap := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey)
	require.NotEmpty(t, snap.EscrowID)
	harness.WarmEscrowOnBothReplicas(t, stack, cfg, snap.EscrowID)

	harness.Step(t, "drive load with parallel lease monitor")
	stopMonitor := make(chan struct{})
	var dupSeen atomic.Bool
	var wgMon sync.WaitGroup
	wgMon.Add(1)
	go func() {
		defer wgMon.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ticker.C:
				s, err := stack.TryPostgresLeaseSnapshot(cfg)
				if err != nil {
					t.Logf("citest: monitor snapshot: %v", err)
					continue
				}
				if s.DuplicateGroups > 0 {
					dupSeen.Store(true)
					t.Logf("citest: monitor saw duplicate_groups=%d total=%d", s.DuplicateGroups, s.Total)
					return
				}
			}
		}
	}()

	harness.DriveLeaseRaceLoad(t, client, eps.GatewayHTTP, model, 24, 8)
	close(stopMonitor)
	wgMon.Wait()
	require.False(t, dupSeen.Load(), "duplicate lease groups observed during load")

	final := harness.WaitLeaseTerminal(t, stack, cfg, 1, 2*time.Minute)
	harness.RequireLeaseExclusivityPass(t, final, 5)
	harness.RequireMockValidationsPassed(t, stack, cfg)
}

// validateHoldLatencyMs is long enough that pending stays elevated through the
// stretch window + first-token follow-up, then drains after ResetMockOpenAIFault.
const validateHoldLatencyMs = 12_000

// pendingStretchHold is sampled exclusivity while Validate is still in-flight.
// Must stay well below validateHoldLatencyMs so pending does not drain mid-hold.
const pendingStretchHold = 3 * time.Second

// TestValidationLeaseRacePendingStretch covers manual plan §7a on one shared
// stack: Validate-only SlowMockOpenAI holds pending with exclusivity, Execute
// stays first-token healthy, then pending drains after fault reset.
//
// Subtests:
//   - streamClient — client stream API (interchangeable under always-stream)
//   - nonStreamClientAlwaysStream — non-stream client under pinned ForceUpstreamStreaming
//     (the path that used to 502 when latency_ms leaked onto stream ML calls)
func TestValidationLeaseRacePendingStretch(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootValidationLeaseRaceStack(t, "citest-validation-lease-race-pending-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "mock-openai-0", "devshard-postgres")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)
	dapi := harness.MockDAPIFromEndpoints(eps)
	harness.SetValidationRate100(t, client, dapi.HTTP)

	model := config.PrimaryModelID(cfg)
	seed := harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "citest validation lease race 7a seed"}},
		MaxTokens: 16,
	}
	harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, seed)
	escrow := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey).EscrowID
	harness.WarmEscrowOnBothReplicas(t, stack, cfg, escrow)

	t.Run("streamClient", func(t *testing.T) {
		runPendingStretchOnStack(t, stack, cfg, eps, client, mockOpenAI, model, "7a-stream", true)
	})
	t.Run("nonStreamClientAlwaysStream", func(t *testing.T) {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		harness.WaitLeasePendingZero(t, stack, cfg, 2*time.Minute)
		runPendingStretchOnStack(t, stack, cfg, eps, client, mockOpenAI, model, "7a-always-stream", false)
	})
}

func runPendingStretchOnStack(
	t *testing.T,
	stack *harness.Stack,
	cfg *config.File,
	eps harness.Endpoints,
	client *http.Client,
	mockOpenAI, model, label string,
	useStreamClient bool,
) {
	t.Helper()

	harness.Step(t, "%s: SlowMockOpenAI Validate latency=%dms, stream first-token immediate", label, validateHoldLatencyMs)
	harness.SlowMockOpenAI(t, client, mockOpenAI, validateHoldLatencyMs)

	var wg sync.WaitGroup
	var loadOK, loadErrs, firstTokenOK atomic.Int32
	var fastestFirstToken atomic.Int64 // nanoseconds; 0 = none
	const nLoad = 4
	for i := 0; i < nLoad; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: fmt.Sprintf("citest validation lease race %s slow %d", label, i)},
				},
				MaxTokens: 16,
			}
			slowClient := harness.GatewayChatClient()
			slowClient.Timeout = 3 * time.Minute
			if useStreamClient {
				res, err := harness.TryPostGatewayChatCompletionStreamTimed(slowClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
				if err != nil {
					loadErrs.Add(1)
					t.Logf("citest: %s chat %d: %v", label, i, err)
					return
				}
				recordFirstToken(&firstTokenOK, &fastestFirstToken, res.FirstToken)
				loadOK.Add(1)
				return
			}
			// Non-stream client under always-stream: full JSON OK implies gateway
			// got first token; dedicated stream probes below measure TTFT explicitly.
			if _, err := harness.TryPostGatewayChatCompletion(slowClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req); err != nil {
				loadErrs.Add(1)
				t.Logf("citest: %s chat %d: %v", label, i, err)
				return
			}
			loadOK.Add(1)
		}(i)
	}
	if !useStreamClient {
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req := harness.ChatCompletionRequest{
					Model: model,
					Messages: []harness.ChatMessage{
						{Role: "user", Content: fmt.Sprintf("citest validation lease race %s ttft %d", label, i)},
					},
					MaxTokens: 16,
				}
				probe := harness.GatewayChatClient()
				probe.Timeout = 3 * time.Minute
				res, err := harness.TryPostGatewayChatCompletionStreamTimed(probe, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
				if err != nil {
					t.Logf("citest: %s ttft probe %d: %v", label, i, err)
					return
				}
				recordFirstToken(&firstTokenOK, &fastestFirstToken, res.FirstToken)
			}(i)
		}
	}

	pending := harness.WaitLeasePending(t, stack, cfg, 1, 45*time.Second)
	require.Equal(t, 0, pending.DuplicateGroups)
	harness.RequireLeaseOneOwnerPerInference(t, pending)
	harness.Step(t, "%s: observed pending=%d; holding stretch with exclusivity", label, pending.Pending)

	var sawOKWhilePending, sawFirstTokenWhilePending atomic.Bool
	peak := harness.HoldLeasePendingStretch(t, stack, cfg, 1, pendingStretchHold, 500*time.Millisecond, func(_ harness.LeaseSnapshot) {
		if loadOK.Load() > 0 {
			sawOKWhilePending.Store(true)
		}
		if firstTokenOK.Load() > 0 {
			sawFirstTokenWhilePending.Store(true)
		}
	})

	// Validate latency is 12s; stretch is 3s. Keep requiring pending≥1 until we
	// have seen Execute progress (first token and full chat OK).
	followUp := time.Duration(validateHoldLatencyMs)*time.Millisecond - pendingStretchHold - 2*time.Second
	if followUp < 3*time.Second {
		followUp = 3 * time.Second
	}
	if !sawOKWhilePending.Load() || !sawFirstTokenWhilePending.Load() {
		ok := harness.AssertEventually(t, followUp, 250*time.Millisecond, func() bool {
			snap, err := stack.TryPostgresLeaseSnapshot(cfg)
			if err != nil || snap.Pending < 1 {
				return false
			}
			if loadOK.Load() > 0 {
				sawOKWhilePending.Store(true)
			}
			if firstTokenOK.Load() > 0 {
				sawFirstTokenWhilePending.Store(true)
			}
			return sawOKWhilePending.Load() && sawFirstTokenWhilePending.Load()
		})
		require.True(t, ok,
			"%s: expected chat OK + first-token while pending≥1 (ok=%d err=%d firstToken=%d)",
			label, loadOK.Load(), loadErrs.Load(), firstTokenOK.Load())
	}
	require.True(t, sawOKWhilePending.Load(),
		"%s: Execute must succeed while Validate still holds pending (ok=%d err=%d)",
		label, loadOK.Load(), loadErrs.Load())
	require.True(t, sawFirstTokenWhilePending.Load(),
		"%s: first content token must arrive while pending≥1 (firstTokenOK=%d)",
		label, firstTokenOK.Load())
	ft := time.Duration(fastestFirstToken.Load())
	require.Greater(t, ft, time.Duration(0))
	require.Less(t, ft, harness.GatewayFirstTokenFloor,
		"%s: first token %s should stay under gateway floor %s", label, ft, harness.GatewayFirstTokenFloor)

	harness.Step(t, "%s: reset SlowMockOpenAI; wait pending drain / terminal growth", label)
	harness.ResetMockOpenAIFault(t, client, mockOpenAI)
	drain := harness.WaitLeasePendingDrain(t, stack, cfg, peak.Submitted+peak.Skipped, peak.Pending, 2*time.Minute)
	wg.Wait()

	require.Less(t, loadErrs.Load(), int32(nLoad), "%s: all slow chats failed; cannot trust pending stretch", label)
	harness.RequireLeaseExclusivityPass(t, drain, 1)
}

func recordFirstToken(ok *atomic.Int32, fastestNS *atomic.Int64, d time.Duration) {
	if d <= 0 {
		return
	}
	ok.Add(1)
	for {
		prev := fastestNS.Load()
		if prev != 0 && int64(d) >= prev {
			return
		}
		if fastestNS.CompareAndSwap(prev, int64(d)) {
			return
		}
	}
}

// TestValidationLeaseRaceStaleReclaim covers manual plan §7b: graceful stop
// aborts in-flight Validate and frees the Postgres row; the survivor catch-up
// then re-acquires on the hot path and submits. This is not TTL steal.
func TestValidationLeaseRaceStaleReclaim(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootValidationLeaseRaceStack(t, "citest-validation-lease-race-stale-reclaim-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "mock-openai-0", "devshard-postgres")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health", stack)

	dapi := harness.MockDAPIFromEndpoints(eps)
	harness.SetValidationRate100(t, client, dapi.HTTP)

	model := config.PrimaryModelID(cfg)
	seed := harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "citest validation lease race 7b seed"}},
		MaxTokens: 16,
	}
	harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, seed)
	escrow := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey).EscrowID
	harness.WarmEscrowOnBothReplicas(t, stack, cfg, escrow)

	harness.Step(t, "slow ML so leases stay pending while Validate is in-flight")
	harness.SlowMockOpenAI(t, client, mockOpenAI, 20_000)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slowClient := harness.GatewayChatClient()
			slowClient.Timeout = 3 * time.Minute
			req := harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: fmt.Sprintf("citest validation lease race 7b inflight %d", i)},
				},
				MaxTokens: 16,
			}
			// Non-stream client is fine under SlowMockOpenAI (Validate-only latency).
			// Chat may fail after the replica is stopped; best-effort to create pending leases.
			if _, err := harness.TryPostGatewayChatCompletion(slowClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req); err != nil {
				t.Logf("citest: 7b chat %d: %v", i, err)
			}
		}(i)
	}

	pending := harness.WaitLeasePending(t, stack, cfg, 1, 40*time.Second)
	require.Equal(t, 0, pending.DuplicateGroups)
	harness.RequireLeaseOneOwnerPerInference(t, pending)
	submittedBefore := pending.Submitted
	pendingBefore := pending.Pending
	wantNonce := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey).LatestNonce
	require.Greater(t, wantNonce, uint64(0), "gateway must have durable diffs before the victim is stopped")

	victim := cfg.Hosts[1].ID
	survivor := cfg.Hosts[0].ID
	harness.Step(t, "stop replica %s mid-validate; catch up survivor %s; sibling re-acquires", victim, survivor)
	stack.StopService(t, victim)

	harness.Step(t, "restore ML so survivor Validate can finish after re-acquire")
	harness.ResetMockOpenAIFault(t, client, mockOpenAI)

	// Sticky traffic often lived on the victim. GET /mempool catch-up loads
	// Finished inferences and enqueues validation so Acquire can insert a
	// new pending row (graceful stop already Released the old one).
	harness.WarmEscrowOnHost(t, stack, cfg, survivor, escrow)
	harness.WaitHostDurableNonce(t, stack, cfg, survivor, escrow, wantNonce, 2*time.Minute)

	final := harness.WaitLeaseSubmittedGrowth(t, stack, cfg, submittedBefore+pendingBefore, 2*time.Minute)
	harness.RequireLeaseExclusivityPass(t, final, 1)
	require.GreaterOrEqual(t, final.Submitted, submittedBefore+pendingBefore)
	wg.Wait()
}

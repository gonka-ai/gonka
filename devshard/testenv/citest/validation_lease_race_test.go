//go:build testenvci

package citest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestValidationLeaseRaceCore drives chat load under HA + 100% validation_rate,
// monitors Postgres leases in parallel, and PASS/FAILs on uniqueness (manual plan §§4–6).
func TestValidationLeaseRaceCore(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootValidationLeaseRaceStack(t, "citest-validation-lease-race-core-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "devshardctl", "mock-openai", "devshard-postgres")
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
}

// TestValidationLeaseRacePendingStretch covers manual plan §7a: slow ML keeps
// leases pending while exclusivity still holds.
func TestValidationLeaseRacePendingStretch(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootValidationLeaseRaceStack(t, "citest-validation-lease-race-pending-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "mock-openai", "devshard-postgres")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
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

	harness.Step(t, "slow mock-openai so leases stay pending during Validate")
	harness.SlowMockOpenAI(t, client, mockOpenAI, 12_000)

	var wg sync.WaitGroup
	var loadErrs atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := harness.ChatCompletionRequest{
				Model: model,
				Messages: []harness.ChatMessage{
					{Role: "user", Content: fmt.Sprintf("citest validation lease race 7a slow %d", i)},
				},
				MaxTokens: 16,
			}
			slowClient := harness.GatewayChatClient()
			slowClient.Timeout = 3 * time.Minute
			if _, err := harness.TryPostGatewayChatCompletion(slowClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req); err != nil {
				loadErrs.Add(1)
				t.Logf("citest: 7a chat %d: %v", i, err)
			}
		}(i)
	}

	pending := harness.WaitLeasePending(t, stack, cfg, 1, 45*time.Second)
	require.Equal(t, 0, pending.DuplicateGroups)
	harness.Step(t, "observed pending=%d under slow ML", pending.Pending)

	harness.ResetMockOpenAIFault(t, client, mockOpenAI)
	wg.Wait()
	time.Sleep(2 * time.Second)
	require.Less(t, loadErrs.Load(), int32(4), "all slow chats failed; cannot trust pending stretch")

	final := stack.PostgresLeaseSnapshot(t, cfg)
	harness.RequireLeaseExclusivityPass(t, final, 1)
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
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "mock-openai", "devshard-postgres")
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
			// Chat may fail after the replica is stopped; best-effort to create pending leases.
			if _, err := harness.TryPostGatewayChatCompletion(slowClient, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req); err != nil {
				t.Logf("citest: 7b chat %d: %v", i, err)
			}
		}(i)
	}

	pending := harness.WaitLeasePending(t, stack, cfg, 1, 40*time.Second)
	require.Equal(t, 0, pending.DuplicateGroups)
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

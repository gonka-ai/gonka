//go:build testenvci

package citest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/host"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	snapshotRecoveryWarnThreshold = 30 * time.Second
	snapshotRecoveryFailThreshold = 60 * time.Second
	corruptSnapshotHex            = "6e6f742d612d70726f746f2d736e617073686f74" // "not-a-proto-snapshot"
)

// TestVersiondSnapshotRecoveryUsesSnapshotAfterRestart verifies the production
// versiond stack can recover a long-enough journal through a persisted host
// snapshot instead of replaying from nonce 1.
func TestVersiondSnapshotRecoveryUsesSnapshotAfterRestart(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-versiond-snapshot-recovery-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteMultiConfig(t, stack.WorkDir, harness.MultiConfigOpts{
		Hosts:        2,
		EscrowSlots:  2,
		MaxNonce:     uint32(host.SnapshotInterval * 3),
		EscrowAmount: config.DefaultEscrowAmount * 100,
	})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2, "expected 2 versiond hosts")
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	summary := snapshotRecoverySummary{
		TestName:  t.Name(),
		Hosts:     harness.VersiondHostIDs(cfg),
		Threshold: snapshotRecoveryThresholds(),
	}
	summaryArtifactWritten := false
	defer func() {
		if !summaryArtifactWritten {
			writeSnapshotRecoverySummary(t, summary)
		}
	}()

	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router", "devshard-postgres")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	model := config.PrimaryModelID(cfg)
	snap0 := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.NotEmpty(t, snap0.EscrowID)
	summary.EscrowID = snap0.EscrowID

	targetNonce := nextSnapshotNonce(snap0.LatestNonce)
	summary.TargetSnapshotNonce = targetNonce
	harness.Step(t, "advance gateway session %s to snapshot nonce %d", snap0.EscrowID, targetNonce)
	warmupStartedAt := time.Now()
	snapAtSnapshot := advanceGatewaySessionToNonce(t, chatClient, client, eps.GatewayHTTP, adminKey, model, targetNonce)
	summary.WarmupMs = millisSince(warmupStartedAt)
	summary.LatestNonceBeforeRestart = snapAtSnapshot.LatestNonce
	harness.Step(t, "snapshot warmup finished in %s at latest nonce %d", time.Duration(summary.WarmupMs)*time.Millisecond, snapAtSnapshot.LatestNonce)
	require.Equal(t, snap0.EscrowID, snapAtSnapshot.EscrowID, "escrow id changed while advancing to snapshot")

	snapshotWaitStartedAt := time.Now()
	snapshotNonce := waitPostgresSnapshotNonce(t, stack, cfg, snapAtSnapshot.EscrowID, targetNonce, 3*time.Minute)
	summary.SnapshotWaitMs = millisSince(snapshotWaitStartedAt)
	summary.PostgresSnapshotNonce = snapshotNonce
	require.GreaterOrEqual(t, snapshotNonce, targetNonce)

	hostIDs := harness.VersiondHostIDs(cfg)
	harness.Step(t, "restart all versiond instances (%v) and demand-load the snapshotted session", hostIDs)
	recoveryStartedAt := time.Now()
	harness.RestartServices(t, stack, hostIDs...)
	harness.WaitVersiondSessionHealthy(t, stack, cfg, eps, snapAtSnapshot.EscrowID)
	summary.HostDemandLoadMs = make(map[string]int64, len(hostIDs))
	for _, hostID := range hostIDs {
		summary.HostDemandLoadMs[hostID] = waitVersiondHostSessionMempool(t, stack, hostID, snapAtSnapshot.EscrowID, 3*time.Minute).Milliseconds()
	}
	summary.RestartRecoveryMs = millisSince(recoveryStartedAt)
	harness.Step(t, "versiond restart recovery finished in %s", time.Duration(summary.RestartRecoveryMs)*time.Millisecond)
	if time.Duration(summary.RestartRecoveryMs)*time.Millisecond > snapshotRecoveryWarnThreshold {
		t.Logf("citest: WARNING snapshot restart recovery exceeded warning threshold: got=%s threshold=%s",
			time.Duration(summary.RestartRecoveryMs)*time.Millisecond, snapshotRecoveryWarnThreshold)
	}
	require.LessOrEqual(t, time.Duration(summary.RestartRecoveryMs)*time.Millisecond, snapshotRecoveryFailThreshold,
		"snapshot restart recovery exceeded threshold")

	snapAfterRestart := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	summary.LatestNonceAfterRestart = snapAfterRestart.LatestNonce
	harness.RequireGatewaySessionStable(t, snapAtSnapshot, snapAfterRestart)

	logs, err := stack.ComposeLogsTail(5000, hostIDs...)
	require.NoError(t, err)
	summary.RestoredSnapshotLogSeen = strings.Contains(logs, "restored devshard snapshot") &&
		strings.Contains(logs, "escrow_id="+snapAtSnapshot.EscrowID)
	summary.SnapshotErrorLogSeen = strings.Contains(logs, "failed to load devshard snapshot") ||
		strings.Contains(logs, "failed to decode devshard snapshot") ||
		strings.Contains(logs, "devshard snapshot failed root check")
	require.True(t, summary.RestoredSnapshotLogSeen, "expected restored devshard snapshot log for escrow %s", snapAtSnapshot.EscrowID)
	require.False(t, summary.SnapshotErrorLogSeen, "unexpected snapshot fallback/error log")

	harness.Step(t, "continue gateway chat after snapshot recovery")
	chatAfterRestartStartedAt := time.Now()
	resp := harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest versiond snapshot recovery after restart"},
		},
		MaxTokens: 4,
	})
	summary.PostRestartChatMs = millisSince(chatAfterRestartStartedAt)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	snapAfterChat := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	summary.LatestNonceAfterChat = snapAfterChat.LatestNonce
	harness.RequireGatewaySessionAdvanced(t, snapAfterRestart, snapAfterChat)

	if artifactDir, ok := writeSnapshotRecoverySummary(t, summary); ok {
		summaryArtifactWritten = true
		validateSnapshotRecoveryArtifacts(t, artifactDir, summary)
	}
}

// TestVersiondSnapshotRecoveryCorruptSnapshotFallsBack verifies a corrupt
// persisted snapshot does not strand a production versiond host: recovery logs
// the rejected snapshot, replays the journal from nonce 1, repairs the snapshot,
// and the session remains usable.
func TestVersiondSnapshotRecoveryCorruptSnapshotFallsBack(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-versiond-corrupt-snapshot-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteMultiConfig(t, stack.WorkDir, harness.MultiConfigOpts{
		Hosts:        2,
		EscrowSlots:  2,
		MaxNonce:     uint32(host.SnapshotInterval * 3),
		EscrowAmount: config.DefaultEscrowAmount * 100,
	})
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2, "expected 2 versiond hosts")
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)

	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router", "devshard-postgres")
		}
	})

	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	model := config.PrimaryModelID(cfg)
	snap0 := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	require.NotEmpty(t, snap0.EscrowID)

	targetNonce := nextSnapshotNonce(snap0.LatestNonce)
	harness.Step(t, "advance gateway session %s to snapshot nonce %d", snap0.EscrowID, targetNonce)
	snapAtSnapshot := advanceGatewaySessionToNonce(t, chatClient, client, eps.GatewayHTTP, adminKey, model, targetNonce)
	require.Equal(t, snap0.EscrowID, snapAtSnapshot.EscrowID, "escrow id changed while advancing to snapshot")
	snapshotNonce := waitPostgresSnapshotNonce(t, stack, cfg, snapAtSnapshot.EscrowID, targetNonce, 3*time.Minute)
	require.GreaterOrEqual(t, snapshotNonce, targetNonce)

	corruptPostgresSnapshot(t, stack, cfg, snapAtSnapshot.EscrowID)

	hostIDs := harness.VersiondHostIDs(cfg)
	recoveryHost := hostIDs[0]
	harness.Step(t, "restart %s and demand-load session %s with corrupt snapshot", recoveryHost, snapAtSnapshot.EscrowID)
	recoveryStartedAt := time.Now()
	harness.RestartService(t, stack, recoveryHost)
	elapsed := waitVersiondHostSessionMempool(t, stack, recoveryHost, snapAtSnapshot.EscrowID, 3*time.Minute)
	harness.Step(t, "%s corrupt snapshot fallback finished in %s (direct load %s)", recoveryHost, time.Since(recoveryStartedAt), elapsed)
	require.LessOrEqual(t, time.Since(recoveryStartedAt), snapshotRecoveryFailThreshold,
		"corrupt snapshot fallback exceeded threshold")

	logs, err := stack.ComposeLogsTail(5000, recoveryHost)
	require.NoError(t, err)
	require.Contains(t, logs, "failed to decode devshard snapshot, replaying full history")
	require.Contains(t, logs, "escrow_id="+snapAtSnapshot.EscrowID)
	require.NotContains(t, logs, "restored devshard snapshot")
	requirePostgresSnapshotRepaired(t, stack, cfg, snapAtSnapshot.EscrowID)

	snapAfterFallback := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionStable(t, snapAtSnapshot, snapAfterFallback)

	harness.Step(t, "continue gateway chat after corrupt snapshot fallback")
	resp := harness.PostGatewayChatCompletion(t, chatClient, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest versiond corrupt snapshot fallback after restart"},
		},
		MaxTokens: 4,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	snapAfterChat := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, adminKey)
	harness.RequireGatewaySessionAdvanced(t, snapAfterFallback, snapAfterChat)
}

type snapshotRecoverySummary struct {
	TestName                 string                  `json:"test_name"`
	EscrowID                 string                  `json:"escrow_id,omitempty"`
	Hosts                    []string                `json:"hosts,omitempty"`
	TargetSnapshotNonce      uint64                  `json:"target_snapshot_nonce,omitempty"`
	PostgresSnapshotNonce    uint64                  `json:"postgres_snapshot_nonce,omitempty"`
	LatestNonceBeforeRestart uint64                  `json:"latest_nonce_before_restart,omitempty"`
	LatestNonceAfterRestart  uint64                  `json:"latest_nonce_after_restart,omitempty"`
	LatestNonceAfterChat     uint64                  `json:"latest_nonce_after_chat,omitempty"`
	WarmupMs                 int64                   `json:"warmup_ms,omitempty"`
	SnapshotWaitMs           int64                   `json:"snapshot_wait_ms,omitempty"`
	RestartRecoveryMs        int64                   `json:"restart_recovery_ms,omitempty"`
	HostDemandLoadMs         map[string]int64        `json:"host_demand_load_ms,omitempty"`
	PostRestartChatMs        int64                   `json:"post_restart_chat_ms,omitempty"`
	RestoredSnapshotLogSeen  bool                    `json:"restored_snapshot_log_seen"`
	SnapshotErrorLogSeen     bool                    `json:"snapshot_error_log_seen"`
	Threshold                snapshotRecoveryBudgets `json:"threshold"`
}

type snapshotRecoveryBudgets struct {
	RestartRecoveryWarnMs int64 `json:"restart_recovery_warn_ms"`
	RestartRecoveryFailMs int64 `json:"restart_recovery_fail_ms"`
}

func snapshotRecoveryThresholds() snapshotRecoveryBudgets {
	return snapshotRecoveryBudgets{
		RestartRecoveryWarnMs: snapshotRecoveryWarnThreshold.Milliseconds(),
		RestartRecoveryFailMs: snapshotRecoveryFailThreshold.Milliseconds(),
	}
}

func millisSince(startedAt time.Time) int64 {
	return time.Since(startedAt).Milliseconds()
}

func writeSnapshotRecoverySummary(t *testing.T, summary snapshotRecoverySummary) (string, bool) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("TESTENV_CITEST_ARTIFACT_DIR"))
	if dir == "" {
		return "", false
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := json.MarshalIndent(summary, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(dir, "versiond-snapshot-recovery-summary.json")
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
	t.Logf("citest: wrote snapshot recovery summary artifact %s", path)
	return dir, true
}

func validateSnapshotRecoveryArtifacts(t *testing.T, artifactDir string, want snapshotRecoverySummary) {
	t.Helper()
	path := filepath.Join(artifactDir, "versiond-snapshot-recovery-summary.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, json.Valid(data), "summary artifact is not valid JSON: %s", path)

	var got snapshotRecoverySummary
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, want.TestName, got.TestName)
	require.Equal(t, want.EscrowID, got.EscrowID)
	require.Equal(t, want.Hosts, got.Hosts)
	require.Equal(t, want.TargetSnapshotNonce, got.TargetSnapshotNonce)
	require.Equal(t, want.PostgresSnapshotNonce, got.PostgresSnapshotNonce)
	require.Equal(t, want.LatestNonceBeforeRestart, got.LatestNonceBeforeRestart)
	require.Equal(t, want.LatestNonceAfterRestart, got.LatestNonceAfterRestart)
	require.Equal(t, want.LatestNonceAfterChat, got.LatestNonceAfterChat)
	require.Equal(t, want.HostDemandLoadMs, got.HostDemandLoadMs)
	require.Equal(t, want.Threshold, got.Threshold)

	require.Positive(t, got.WarmupMs)
	require.Positive(t, got.SnapshotWaitMs)
	require.Positive(t, got.RestartRecoveryMs)
	require.Positive(t, got.PostRestartChatMs)
	require.True(t, got.RestoredSnapshotLogSeen)
	require.False(t, got.SnapshotErrorLogSeen)
	require.LessOrEqual(t, time.Duration(got.RestartRecoveryMs)*time.Millisecond, snapshotRecoveryFailThreshold)
	t.Logf("citest: validated snapshot recovery artifact %s", path)
}

func nextSnapshotNonce(latest uint64) uint64 {
	interval := uint64(host.SnapshotInterval)
	return ((latest / interval) + 1) * interval
}

func advanceGatewaySessionToNonce(
	t *testing.T,
	chatClient *http.Client,
	statusClient *http.Client,
	gatewayURL, adminKey, model string,
	targetNonce uint64,
) harness.GatewaySessionSnapshot {
	t.Helper()

	maxRequests := int(host.SnapshotInterval) + 50
	var snap harness.GatewaySessionSnapshot
	for i := 1; i <= maxRequests; i++ {
		resp := harness.PostGatewayChatCompletion(t, chatClient, gatewayURL, adminKey, harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest snapshot recovery warmup %03d", i)},
			},
			MaxTokens: 4,
		})
		harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

		if i == 1 || i%25 == 0 {
			snap = harness.GetGatewaySessionSnapshot(t, statusClient, gatewayURL, adminKey)
			harness.Step(t, "snapshot warmup request %d/%d reached latest nonce %d", i, maxRequests, snap.LatestNonce)
			if snap.LatestNonce >= targetNonce {
				return snap
			}
		}
	}

	snap = harness.GetGatewaySessionSnapshot(t, statusClient, gatewayURL, adminKey)
	require.GreaterOrEqual(t, snap.LatestNonce, targetNonce,
		"gateway session did not reach snapshot nonce after %d requests", maxRequests)
	return snap
}

func waitPostgresSnapshotNonce(t *testing.T, stack *harness.Stack, cfg *config.File, escrowID string, minNonce uint64, timeout time.Duration) uint64 {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastRaw string
	for time.Now().Before(deadline) {
		nonce, raw, err := postgresSnapshotNonce(stack, cfg, escrowID)
		if err == nil && nonce >= minNonce {
			harness.Step(t, "postgres snapshot for escrow %s reached nonce %d", escrowID, nonce)
			return nonce
		}
		lastErr = err
		lastRaw = raw
		time.Sleep(2 * time.Second)
	}

	t.Fatalf("snapshot for escrow %s did not reach nonce %d within %s; lastRaw=%q lastErr=%v",
		escrowID, minNonce, timeout, lastRaw, lastErr)
	return 0
}

func postgresSnapshotNonce(stack *harness.Stack, cfg *config.File, escrowID string) (uint64, string, error) {
	user, db, pass := postgresCreds(cfg)
	query := fmt.Sprintf("SELECT COALESCE(MAX(nonce), 0) FROM devshard_snapshots WHERE escrow_id = '%s'", strings.ReplaceAll(escrowID, "'", "''"))
	raw, err := stack.ComposeExecOutput("devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At", "-c", query)
	if err != nil {
		return 0, raw, err
	}
	nonce, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, raw, fmt.Errorf("parse snapshot nonce %q: %w", raw, err)
	}
	return nonce, raw, nil
}

func corruptPostgresSnapshot(t *testing.T, stack *harness.Stack, cfg *config.File, escrowID string) {
	t.Helper()
	user, db, pass := postgresCreds(cfg)
	query := fmt.Sprintf("UPDATE devshard_snapshots SET state_data = decode('%s', 'hex') WHERE escrow_id = '%s'",
		corruptSnapshotHex, strings.ReplaceAll(escrowID, "'", "''"))
	raw := stack.ComposeExec(t, "devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At", "-c", query)
	require.Contains(t, raw, "UPDATE 1")
	harness.Step(t, "corrupted postgres snapshot for escrow %s", escrowID)
}

func requirePostgresSnapshotRepaired(t *testing.T, stack *harness.Stack, cfg *config.File, escrowID string) {
	t.Helper()
	user, db, pass := postgresCreds(cfg)
	query := fmt.Sprintf("SELECT encode(state_data, 'hex') FROM devshard_snapshots WHERE escrow_id = '%s'",
		strings.ReplaceAll(escrowID, "'", "''"))
	raw := stack.ComposeExec(t, "devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At", "-c", query)
	require.NotEqual(t, corruptSnapshotHex, strings.TrimSpace(raw), "corrupt snapshot was not repaired")
	harness.Step(t, "postgres snapshot for escrow %s was repaired after full replay fallback", escrowID)
}

func postgresCreds(cfg *config.File) (user, db, pass string) {
	user = config.DefaultPostgresUser
	db = config.DefaultPostgresDB
	pass = config.DefaultPostgresPassword
	if cfg != nil {
		if cfg.Postgres.User != "" {
			user = cfg.Postgres.User
		}
		if cfg.Postgres.Database != "" {
			db = cfg.Postgres.Database
		}
		if cfg.Postgres.Password != "" {
			pass = cfg.Postgres.Password
		}
	}
	return user, db, pass
}

func waitVersiondHostSessionMempool(t *testing.T, stack *harness.Stack, service, escrowID string, timeout time.Duration) time.Duration {
	t.Helper()

	startedAt := time.Now()
	deadline := time.Now().Add(timeout)
	url := "http://127.0.0.1:8080/sessions/" + escrowID + "/mempool"
	var lastErr error
	var lastRaw string
	for time.Now().Before(deadline) {
		raw, err := stack.ComposeExecOutput(service, "wget", "-q", "-O", "-", url)
		if err == nil {
			elapsed := time.Since(startedAt)
			harness.Step(t, "%s recovered session %s via direct mempool probe in %s", service, escrowID, elapsed)
			return elapsed
		}
		lastErr = err
		lastRaw = raw
		time.Sleep(2 * time.Second)
	}

	t.Fatalf("%s did not serve mempool for session %s within %s; lastRaw=%q lastErr=%v",
		service, escrowID, timeout, lastRaw, lastErr)
	return 0
}

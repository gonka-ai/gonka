package harness

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// LeaseSnapshot is a point-in-time view of devshard_validation_leases.
type LeaseSnapshot struct {
	Total           int
	Pending         int
	Submitted       int
	Skipped         int
	DuplicateGroups int
	Rows            []LeaseRow
}

// LeaseRow is one validation lease.
type LeaseRow struct {
	InferenceID     uint64
	InstanceAddress string
	Status          string
	ClaimedAt       string
}

// PostgresLeaseSnapshot queries shared Postgres for lease exclusivity evidence.
func (s *Stack) PostgresLeaseSnapshot(t *testing.T, cfg *config.File) LeaseSnapshot {
	t.Helper()
	snap, err := s.TryPostgresLeaseSnapshot(cfg)
	require.NoError(t, err)
	return snap
}

// TryPostgresLeaseSnapshot is safe to call from worker goroutines (no testing.T).
func (s *Stack) TryPostgresLeaseSnapshot(cfg *config.File) (LeaseSnapshot, error) {
	user, db, pass := postgresCreds(cfg)
	dupRaw, err := s.ComposeExecOutput("devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At",
		"-c", `SELECT COUNT(*) FROM (
			SELECT epoch_id, escrow_id, inference_id
			FROM devshard_validation_leases
			GROUP BY 1, 2, 3
			HAVING COUNT(*) > 1
		) d`)
	if err != nil {
		return LeaseSnapshot{}, err
	}
	dup, err := strconv.Atoi(strings.TrimSpace(dupRaw))
	if err != nil {
		return LeaseSnapshot{}, fmt.Errorf("parse duplicate_groups %q: %w", dupRaw, err)
	}

	countsRaw, err := s.ComposeExecOutput("devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At", "-F", ",",
		"-c", `SELECT
			COUNT(*)::text,
			COUNT(*) FILTER (WHERE status = 'pending')::text,
			COUNT(*) FILTER (WHERE status = 'submitted')::text,
			COUNT(*) FILTER (WHERE status = 'skipped')::text
		FROM devshard_validation_leases`)
	if err != nil {
		return LeaseSnapshot{}, err
	}
	parts := strings.Split(strings.TrimSpace(countsRaw), ",")
	if len(parts) != 4 {
		return LeaseSnapshot{}, fmt.Errorf("unexpected counts row %q", countsRaw)
	}
	total, _ := strconv.Atoi(parts[0])
	pending, _ := strconv.Atoi(parts[1])
	submitted, _ := strconv.Atoi(parts[2])
	skipped, _ := strconv.Atoi(parts[3])

	rowsRaw, err := s.ComposeExecOutput("devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At", "-F", "|",
		"-c", `SELECT inference_id, instance_address, status, claimed_at
			FROM devshard_validation_leases
			ORDER BY claimed_at DESC LIMIT 50`)
	if err != nil {
		return LeaseSnapshot{}, err
	}
	var rows []LeaseRow
	for _, line := range strings.Split(strings.TrimSpace(rowsRaw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) != 4 {
			return LeaseSnapshot{}, fmt.Errorf("unexpected lease row %q", line)
		}
		inf, err := strconv.ParseUint(cols[0], 10, 64)
		if err != nil {
			return LeaseSnapshot{}, err
		}
		rows = append(rows, LeaseRow{
			InferenceID:     inf,
			InstanceAddress: cols[1],
			Status:          cols[2],
			ClaimedAt:       cols[3],
		})
	}

	return LeaseSnapshot{
		Total:           total,
		Pending:         pending,
		Submitted:       submitted,
		Skipped:         skipped,
		DuplicateGroups: dup,
		Rows:            rows,
	}, nil
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

// RequireLeaseExclusivityPass asserts no duplicate lease keys and enough rows.
func RequireLeaseExclusivityPass(t *testing.T, snap LeaseSnapshot, minLeases int) {
	t.Helper()
	require.Equal(t, 0, snap.DuplicateGroups,
		"duplicate (epoch_id, escrow_id, inference_id) groups: %+v", snap)
	require.GreaterOrEqual(t, snap.Total, minLeases,
		"lease rows=%d < min=%d (pending=%d submitted=%d skipped=%d)",
		snap.Total, minLeases, snap.Pending, snap.Submitted, snap.Skipped)
	t.Logf("citest: lease exclusivity PASS total=%d pending=%d submitted=%d skipped=%d",
		snap.Total, snap.Pending, snap.Submitted, snap.Skipped)
}

// SetValidationRate posts chain params validation_rate (bps) via mock-dapi.
// Call before escrow create so the escrow snapshots the new rate.
func SetValidationRate(t *testing.T, client *http.Client, mockDapiHTTP string, rateBps uint32) {
	t.Helper()
	rate := rateBps
	PatchTestenvParams(t, client, mockDapiHTTP, adminface.ParamsRequest{ValidationRate: &rate})
}

// SetValidationRate100 configures a 100% rate for dense lease races.
func SetValidationRate100(t *testing.T, client *http.Client, mockDapiHTTP string) {
	t.Helper()
	SetValidationRate(t, client, mockDapiHTTP, 10000)
}

// WarmEscrowOnBothReplicas GETs session /mempool on each versiond so lazy load /
// RecoverSessions brings the Host up on every HA replica (sticky router alone
// would keep one cold). Session routes have no /healthz — see WaitVersiondSessionHealthy.
func WarmEscrowOnBothReplicas(t *testing.T, stack *Stack, cfg *config.File, escrowID string) {
	t.Helper()
	for _, h := range cfg.Hosts {
		WarmEscrowOnHost(t, stack, cfg, h.ID, escrowID)
	}
}

// WarmEscrowOnHost GETs /mempool on one versiond to lazy-load / catch up the Host.
func WarmEscrowOnHost(t *testing.T, stack *Stack, cfg *config.File, hostID, escrowID string) {
	t.Helper()
	require.NotEmpty(t, escrowID)
	require.NotEmpty(t, hostID)
	ver := cfg.Versiond.VersionName
	url := fmt.Sprintf("http://%s:8080/%s/sessions/%s/mempool", hostID, ver, escrowID)
	out := stack.ComposeExec(t, "mock-chain", "wget", "-q", "-O", "-", url)
	t.Logf("citest: warm %s → %s (%d bytes)", hostID, url, len(out))
}

// DriveLeaseRaceLoad posts non-stream + stream chats through the gateway.
func DriveLeaseRaceLoad(t *testing.T, client *http.Client, gatewayURL, model string, nonStream, stream int) {
	t.Helper()
	if client == nil {
		client = GatewayChatClient()
	}
	adminKey := TestenvAdminAPIKey
	for i := 0; i < nonStream; i++ {
		req := ChatCompletionRequest{
			Model: model,
			Messages: []ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest validation lease race non-stream %d", i)},
			},
			MaxTokens: 32,
		}
		resp := PostGatewayChatCompletion(t, client, gatewayURL, adminKey, req)
		RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	}
	for i := 0; i < stream; i++ {
		req := ChatCompletionRequest{
			Model: model,
			Messages: []ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest validation lease race stream %d", i)},
			},
			MaxTokens: 32,
		}
		content, _ := PostGatewayChatCompletionStream(t, client, gatewayURL, adminKey, req)
		RequireMockOpenAIContent(t, content)
	}
}

// PatchVersiondLeaseTTL sets lease TTL + retry interval on all versiond services.
func PatchVersiondLeaseTTL(t *testing.T, composePath, leaseTTL, retryInterval string) {
	t.Helper()
	PatchComposeEnvKey(t, composePath, "DEVSHARD_VALIDATION_LEASE_TTL", leaseTTL)
	PatchComposeEnvKey(t, composePath, "DEVSHARD_VALIDATION_RETRY_INTERVAL", retryInterval)
}

// PauseMockOpenAIBlocks validation ML with HTTP 503 (leaves Postgres leases pending).
func PauseMockOpenAI(t *testing.T, client *http.Client, mockOpenAIURL string) {
	t.Helper()
	status := 503
	PatchMockOpenAIFault(t, client, mockOpenAIURL, mockopenai.FaultPatch{HTTPStatus: &status})
}

// SlowMockOpenAI stretches Validate so leases stay pending longer.
func SlowMockOpenAI(t *testing.T, client *http.Client, mockOpenAIURL string, latencyMs int) {
	t.Helper()
	PatchMockOpenAIFault(t, client, mockOpenAIURL, mockopenai.FaultPatch{LatencyMs: &latencyMs})
}

// WaitLeasePending polls until at least minPending leases are pending (or timeout).
func WaitLeasePending(t *testing.T, stack *Stack, cfg *config.File, minPending int, timeout time.Duration) LeaseSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last LeaseSnapshot
	for time.Now().Before(deadline) {
		last = stack.PostgresLeaseSnapshot(t, cfg)
		if last.Pending >= minPending {
			return last
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("citest: pending leases=%d < %d after %s (total=%d)", last.Pending, minPending, timeout, last.Total)
	return last
}

// WaitLeaseSubmittedGrowth waits until submitted count is >= minSubmitted.
func WaitLeaseSubmittedGrowth(t *testing.T, stack *Stack, cfg *config.File, minSubmitted int, timeout time.Duration) LeaseSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last LeaseSnapshot
	for time.Now().Before(deadline) {
		last = stack.PostgresLeaseSnapshot(t, cfg)
		require.Equal(t, 0, last.DuplicateGroups, "duplicates while waiting for submit")
		if last.Submitted >= minSubmitted {
			return last
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("citest: submitted=%d < %d after %s (pending=%d total=%d)",
		last.Submitted, minSubmitted, timeout, last.Pending, last.Total)
	return last
}

// WaitLeaseTerminal waits until submitted+skipped >= minTerminal (validations finish).
func WaitLeaseTerminal(t *testing.T, stack *Stack, cfg *config.File, minTerminal int, timeout time.Duration) LeaseSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last LeaseSnapshot
	for time.Now().Before(deadline) {
		last = stack.PostgresLeaseSnapshot(t, cfg)
		require.Equal(t, 0, last.DuplicateGroups, "duplicates while waiting for terminal leases")
		if last.Submitted+last.Skipped >= minTerminal {
			return last
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("citest: terminal(submitted+skipped)=%d < %d after %s (pending=%d total=%d)",
		last.Submitted+last.Skipped, minTerminal, timeout, last.Pending, last.Total)
	return last
}

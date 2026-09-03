package harness

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"devshard/testenv/config"
)

// BootSQLiteHAMigrationStack boots the 2×versiond + Postgres stack patched for
// §3.3 Phase 0–1: DEVSHARD_STORAGE_MODE=sqlite, GONKA_HA empty so the router
// does not stamp Devshard-Ha, and VERSIOND_NON_HA_VERSIONS pinned to the running
// VersionName (a real child — HAProxy L7-checks /{version}/healthz). versiond-1
// is stopped so SQLite sessions land on the legacy host volume. The pin is
// applied before Up so phase 0 does not recreate the router while the gateway
// is seeding.
func BootSQLiteHAMigrationStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	RequireLinuxDevshardd(t, stack.TestenvDir)
	WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	requireTwoVersiondHosts(t, cfg)

	PatchVersiondStorageMode(t, stack.ComposePath, "sqlite")
	PatchRouterHADeployment(t, stack.ComposePath, false)
	require.NotEmpty(t, cfg.Versiond.VersionName)
	PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", fmt.Sprintf("%q", cfg.Versiond.VersionName))

	stack.Up(t)
	stack.StopService(t, cfg.Hosts[1].ID)
	return stack, cfg, stack.Endpoints(t, cfg)
}

// RecreateRouterNonHAVersions sets VERSIOND_NON_HA_VERSIONS and force-recreates
// versiond-router. Empty versions clears the pin. Host-published ports remap.
func RecreateRouterNonHAVersions(t *testing.T, stack *Stack, cfg *config.File, versions string) Endpoints {
	t.Helper()
	value := `""`
	if versions != "" {
		value = fmt.Sprintf("%q", versions)
	}
	PatchComposeEnvKey(t, stack.ComposePath, "VERSIOND_NON_HA_VERSIONS", value)
	stack.RecreateServices(t, "versiond-router")
	return stack.Endpoints(t, cfg)
}

// RecreateServices force-recreates named services (picks up compose env changes).
// Does not re-register Down cleanup (Up already did).
func (s *Stack) RecreateServices(t *testing.T, services ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()

	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "up", "-d", "--wait", "--force-recreate", "--no-deps")
	args = append(args, services...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		DumpComposeLogs(t, s, services...)
		t.Fatalf("docker compose recreate %v: %v\n%s", services, err, out)
	}
}

// ComposeExec runs `docker compose exec -T service cmd...` and returns stdout+stderr.
func (s *Stack) ComposeExec(t *testing.T, service string, cmdArgs ...string) string {
	t.Helper()
	out, err := s.ComposeExecOutput(service, cmdArgs...)
	require.NoError(t, err, "compose exec %s %v\n%s", service, cmdArgs, out)
	return out
}

// ComposeExecOutput runs `docker compose exec -T service cmd...` and returns stdout+stderr.
func (s *Stack) ComposeExecOutput(service string, cmdArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{"compose"}, s.composeFileArgs()...)
	args = append(args, "exec", "-T", service)
	args = append(args, cmdArgs...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = s.WorkDir
	cmd.Env = s.composeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("compose exec %s %v: %w\n%s", service, cmdArgs, err, out)
	}
	return string(out), nil
}

// VersionStoreDir is the host path for a versiond child's per-version data dir.
func VersionStoreDir(stack *Stack, hostID, versionName string) string {
	return filepath.Join(stack.WorkDir, "data", hostID, versionName)
}

// SQLiteSessionInventory is a Phase-1 snapshot of local SQLite session ownership.
type SQLiteSessionInventory struct {
	StoreDir  string
	EscrowIDs []string
	EpochByID map[string]int64
}

// InventorySQLiteVersionStore reads escrow_epoch from {storeDir}/_meta.db.
func InventorySQLiteVersionStore(t *testing.T, storeDir string) SQLiteSessionInventory {
	t.Helper()
	metaPath := filepath.Join(storeDir, "_meta.db")
	require.FileExists(t, metaPath, "expected SQLite meta at %s", metaPath)

	db, err := sql.Open("sqlite", metaPath)
	require.NoError(t, err)
	defer db.Close()

	rows, err := db.Query(`SELECT escrow_id, epoch_id FROM escrow_epoch ORDER BY escrow_id`)
	require.NoError(t, err)
	defer rows.Close()

	inv := SQLiteSessionInventory{
		StoreDir:  storeDir,
		EpochByID: make(map[string]int64),
	}
	for rows.Next() {
		var id string
		var epoch int64
		require.NoError(t, rows.Scan(&id, &epoch))
		inv.EscrowIDs = append(inv.EscrowIDs, id)
		inv.EpochByID[id] = epoch
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, inv.EscrowIDs, "expected at least one SQLite session in %s", storeDir)
	return inv
}

// RequireSQLiteQuarantined asserts boot-migrate renamed local SQLite artifacts.
func RequireSQLiteQuarantined(t *testing.T, storeDir string) {
	t.Helper()
	entries, err := os.ReadDir(storeDir)
	require.NoError(t, err)
	var migrated int
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, ".migrated.") {
			migrated++
		}
		require.False(t, name == "_meta.db", "live _meta.db should be quarantined after postgres migrate")
		require.False(t, strings.HasPrefix(name, "epoch_") && strings.HasSuffix(name, ".db"),
			"live epoch db %s should be quarantined", name)
	}
	require.Greater(t, migrated, 0, "expected *.migrated.* artifacts under %s", storeDir)
}

// RequirePGBoundMarker asserts .pg-bound exists after Postgres bind.
func RequirePGBoundMarker(t *testing.T, storeDir string) {
	t.Helper()
	require.FileExists(t, filepath.Join(storeDir, ".pg-bound"))
}

// PostgresSessionIndex lists escrow_id → epoch_id from shared Postgres.
func (s *Stack) PostgresSessionIndex(t *testing.T, cfg *config.File) map[string]int64 {
	t.Helper()
	user := cfg.Postgres.User
	if user == "" {
		user = config.DefaultPostgresUser
	}
	db := cfg.Postgres.Database
	if db == "" {
		db = config.DefaultPostgresDB
	}
	pass := cfg.Postgres.Password
	if pass == "" {
		pass = config.DefaultPostgresPassword
	}

	out := s.ComposeExec(t, "devshard-postgres",
		"env", "PGPASSWORD="+pass,
		"psql", "-U", user, "-d", db, "-At", "-F", ",",
		"-c", "SELECT escrow_id, epoch_id FROM devshard_session_index ORDER BY escrow_id")

	got := make(map[string]int64)
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		require.Len(t, parts, 2, "unexpected psql row %q", line)
		var epoch int64
		_, err := fmt.Sscanf(parts[1], "%d", &epoch)
		require.NoError(t, err)
		got[parts[0]] = epoch
	}
	return got
}

// RequirePostgresMatchesInventory asserts PG session index covers the SQLite snapshot.
func RequirePostgresMatchesInventory(t *testing.T, stack *Stack, cfg *config.File, inv SQLiteSessionInventory) {
	t.Helper()
	pg := stack.PostgresSessionIndex(t, cfg)
	require.GreaterOrEqual(t, len(pg), len(inv.EscrowIDs),
		"postgres session index smaller than sqlite inventory: pg=%v sqlite=%v", pg, inv.EscrowIDs)
	for _, id := range inv.EscrowIDs {
		epoch, ok := pg[id]
		require.True(t, ok, "escrow %s missing from postgres session index %v", id, pg)
		require.Equal(t, inv.EpochByID[id], epoch, "epoch mismatch for escrow %s", id)
	}
}

// WaitRoutedStatus polls until GET returns an allowed status with
// X-Versiond-Backend set. HAProxy catalog NOSRV is also 503 but has no routing
// headers; this wait skips that empty 503.
func WaitRoutedStatus(t *testing.T, client *http.Client, url string, timeout time.Duration, label string, allowed ...int) int {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	want := make(map[int]struct{}, len(allowed))
	for _, c := range allowed {
		want[c] = struct{}{}
	}
	var last int
	var lastBackend string
	var lastErr string
	ok := AssertEventually(t, timeout, time.Second, func() bool {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err.Error()
			return false
		}
		_ = resp.Body.Close()
		last = resp.StatusCode
		lastBackend = resp.Header.Get("X-Versiond-Backend")
		if lastBackend == "" {
			lastErr = fmt.Sprintf("HTTP %d (no X-Versiond-Backend)", last)
			return false
		}
		_, hit := want[last]
		if !hit {
			lastErr = fmt.Sprintf("HTTP %d backend=%s", last, lastBackend)
		}
		return hit
	})
	require.True(t, ok, "%s: never reached statuses %v with routing headers from %s (last=%d backend=%q err=%s)",
		label, allowed, url, last, lastBackend, lastErr)
	return last
}

// WaitGETStatus polls until GET returns one of the allowed status codes.
func WaitGETStatus(t *testing.T, client *http.Client, url string, timeout time.Duration, label string, allowed ...int) int {
	t.Helper()
	if client == nil {
		client = HTTPClient()
	}
	want := make(map[int]struct{}, len(allowed))
	for _, c := range allowed {
		want[c] = struct{}{}
	}
	var last int
	var lastErr string
	ok := AssertEventually(t, timeout, time.Second, func() bool {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err.Error()
			return false
		}
		_ = resp.Body.Close()
		last = resp.StatusCode
		_, hit := want[resp.StatusCode]
		if !hit {
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return hit
	})
	require.True(t, ok, "%s: never reached statuses %v from %s (last=%d err=%s)", label, allowed, url, last, lastErr)
	return last
}

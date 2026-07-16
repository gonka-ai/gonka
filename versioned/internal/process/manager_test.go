package process

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/download"
	"versioned/internal/oracle"
)

func TestChildEnvIncludesVersionLogPrefix(t *testing.T) {
	env := childEnv("0.2.13-v2-r2", "")
	want := map[string]bool{
		"DEVSHARD_BINARY_LOG_VERSION=0.2.13-v2-r2": false,
	}
	for _, entry := range env {
		if _, ok := want[entry]; ok {
			want[entry] = true
		}
	}
	for key, present := range want {
		if !present {
			t.Fatalf("childEnv missing %q", key)
		}
	}
}

func TestChildEnvSlotNameFallback(t *testing.T) {
	env := childEnv("v2", "")
	want := "DEVSHARD_BINARY_LOG_VERSION=v2"
	found := false
	for _, entry := range env {
		if entry == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("childEnv missing %q", want)
	}
}

func TestChildEnvIncludesAdminAddr(t *testing.T) {
	env := childEnv("v2", "127.0.0.1:6001")
	want := map[string]bool{
		"DEVSHARD_BINARY_LOG_VERSION=v2":     false,
		"DEVSHARD_ADMIN_ADDR=127.0.0.1:6001": false,
	}
	for _, entry := range env {
		if _, ok := want[entry]; ok {
			want[entry] = true
		}
	}
	for key, present := range want {
		if !present {
			t.Fatalf("childEnv missing %q", key)
		}
	}
}

func TestChildEnvDoesNotLeakParentAdminAddr(t *testing.T) {
	t.Setenv("DEVSHARD_ADMIN_ADDR", "127.0.0.1:9999")

	env := childEnv("v2", "")
	for _, entry := range env {
		if strings.HasPrefix(entry, "DEVSHARD_ADMIN_ADDR=") {
			t.Fatalf("childEnv leaked parent %q", entry)
		}
	}
}

func TestPreflightChild_MissingBinary(t *testing.T) {
	_, err := preflightChild(filepath.Join(t.TempDir(), "missing"), "v2")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestPreflightChild_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "legacy-bin")
	script := `#!/bin/sh
echo "unknown flag: $1" >&2
exit 2
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChild(binPath, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if preflight.binaryLogVersion != "v2" {
		t.Fatalf("legacy preflight binaryLogVersion = %q, want %q", preflight.binaryLogVersion, "v2")
	}
}

func TestPreflightChild_ProtocolFlagUnsupported(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "binary-only-stamp")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChild(binPath, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if preflight.binaryLogVersion != "0.2.13-v2-r2" {
		t.Fatalf("binaryLogVersion = %q, want stamped build id", preflight.binaryLogVersion)
	}
}

func TestPreflightChild_BinaryFlagUnsupported(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "protocol-only-stamp")
	script := `#!/bin/sh
case "$1" in
--print-protocol-version) echo "v2" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChild(binPath, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if preflight.binaryLogVersion != "v2" {
		t.Fatalf("binaryLogVersion = %q, want slot name fallback %q", preflight.binaryLogVersion, "v2")
	}
}

func TestPreflightChild_ProtocolMismatch(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "stamped-bin")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) echo "v1" ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := preflightChild(binPath, "v2")
	if err == nil {
		t.Fatal("expected protocol mismatch error")
	}
}

func TestPreflightChild_StampedBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "stamped-bin")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) echo "v2" ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChild(binPath, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if preflight.binaryLogVersion != "0.2.13-v2-r2" {
		t.Fatalf("binaryLogVersion = %q, want %q", preflight.binaryLogVersion, "0.2.13-v2-r2")
	}
}

func TestPreflightChild_AdminAPIUnsupported(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "stamped-bin")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) echo "v2" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChildWithAdminProbe(binPath, "v2", true)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.adminAPISupported {
		t.Fatal("expected unsupported admin API flag to keep public lifecycle fallback")
	}
}

func TestPreflightChild_AdminAPISupported(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "stamped-bin")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) echo "v2" ;;
--print-admin-api-version) echo "1" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChildWithAdminProbe(binPath, "v2", true)
	if err != nil {
		t.Fatal(err)
	}
	if !preflight.adminAPISupported {
		t.Fatal("expected admin API support to be detected")
	}
}

func TestPreflightChild_ProtocolProbeTimeoutFailsClosed(t *testing.T) {
	oldTimeout := embeddedVersionProbeTimeout
	embeddedVersionProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { embeddedVersionProbeTimeout = oldTimeout })

	dir := t.TempDir()
	binPath := filepath.Join(dir, "slow-protocol-stamp")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) sleep 1; echo "v2" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := preflightChild(binPath, "v2"); err == nil {
		t.Fatal("expected timeout probing protocol version to fail closed")
	}
}

func TestNewManager(t *testing.T) {
	cfg := config.Config{
		BinDir:     "/tmp/bin",
		DataDir:    "/tmp/data",
		BinaryName: "testapp",
		BasePort:   5000,
	}
	m := NewManager(cfg)
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	routes := m.RouteTable().Load().(map[string]string)
	if len(routes) != 0 {
		t.Errorf("expected empty routes, got %v", routes)
	}
	status := m.Status()
	if len(status) != 0 {
		t.Errorf("expected empty status, got %v", status)
	}
}

func TestRebuildRoutes(t *testing.T) {
	cfg := config.Config{
		BinDir:     "/tmp/bin",
		DataDir:    "/tmp/data",
		BinaryName: "testapp",
		BasePort:   5000,
	}
	m := NewManager(cfg)

	m.mu.Lock()
	m.processes["v1"] = &child{
		version: oracle.Version{Name: "v1"},
		port:    9001,
		done:    make(chan struct{}),
		status:  statusRunning,
	}
	m.processes["v2"] = &child{
		version: oracle.Version{Name: "v2"},
		port:    9002,
		done:    make(chan struct{}),
		status:  statusRunning,
	}
	m.rebuildRoutes()
	m.mu.Unlock()

	routes := m.RouteTable().Load().(map[string]string)
	if routes["v1"] != "localhost:9001" {
		t.Errorf("v1 route = %q, want %q", routes["v1"], "localhost:9001")
	}
	if routes["v2"] != "localhost:9002" {
		t.Errorf("v2 route = %q, want %q", routes["v2"], "localhost:9002")
	}
}

func TestRebuildRoutes_ExcludesNonRunning(t *testing.T) {
	cfg := config.Config{
		BinDir:     "/tmp/bin",
		DataDir:    "/tmp/data",
		BinaryName: "testapp",
		BasePort:   5000,
	}
	m := NewManager(cfg)

	m.mu.Lock()
	m.processes["v1"] = &child{
		version: oracle.Version{Name: "v1"},
		port:    9001,
		done:    make(chan struct{}),
		status:  statusRunning,
	}
	m.processes["v2"] = &child{
		version: oracle.Version{Name: "v2"},
		port:    9002,
		done:    make(chan struct{}),
		status:  statusStarting,
	}
	m.processes["v3"] = &child{
		version: oracle.Version{Name: "v3"},
		port:    9003,
		done:    make(chan struct{}),
		status:  statusStopped,
	}
	m.rebuildRoutes()
	m.mu.Unlock()

	routes := m.RouteTable().Load().(map[string]string)
	if _, ok := routes["v1"]; !ok {
		t.Error("running v1 should be in routes")
	}
	if _, ok := routes["v2"]; ok {
		t.Error("starting v2 should not be in routes")
	}
	if _, ok := routes["v3"]; ok {
		t.Error("stopped v3 should not be in routes")
	}
}

func TestStatus(t *testing.T) {
	cfg := config.Config{
		BinDir:     "/tmp/bin",
		DataDir:    "/tmp/data",
		BinaryName: "testapp",
		BasePort:   5000,
	}
	m := NewManager(cfg)

	m.mu.Lock()
	m.processes["v1"] = &child{
		version: oracle.Version{Name: "v1"},
		port:    9001,
		done:    make(chan struct{}),
		status:  statusRunning,
	}
	m.mu.Unlock()

	statuses := m.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Name != "v1" || statuses[0].Port != 9001 || statuses[0].Status != "running" {
		t.Errorf("status = %+v", statuses[0])
	}
}

func TestStatusIncludesDrainingChildrenButRoutesDoNot(t *testing.T) {
	cfg := config.Config{
		BinDir:     "/tmp/bin",
		DataDir:    "/tmp/data",
		BinaryName: "testapp",
		BasePort:   5000,
	}
	m := NewManager(cfg)

	m.mu.Lock()
	m.processes["v1"] = &child{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: "new-sha",
		binaryVersion: "new-bin",
		port:          9002,
		done:          make(chan struct{}),
		status:        statusRunning,
	}
	m.draining["v1"] = []*child{{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: "old-sha",
		binaryVersion: "old-bin",
		port:          9001,
		done:          make(chan struct{}),
		status:        statusDraining,
	}}
	m.rebuildRoutes()
	m.mu.Unlock()

	routes := m.RouteTable().Load().(map[string]string)
	if routes["v1"] != "localhost:9002" {
		t.Fatalf("route = %q, want new child", routes["v1"])
	}

	statuses := m.Status()
	if len(statuses) != 2 {
		t.Fatalf("expected running + draining status, got %d", len(statuses))
	}
	var sawDraining bool
	for _, status := range statuses {
		if status.Status == statusDraining && status.Port == 9001 && status.SHA256 == "old-sha" {
			sawDraining = true
		}
	}
	if !sawDraining {
		t.Fatalf("draining child not reported in status: %+v", statuses)
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")
	content := []byte("hello world")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := download.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(content)
	want := hex.EncodeToString(h[:])
	if got != want {
		t.Errorf("hashFile = %q, want %q", got, want)
	}
}

func TestHashFile_Missing(t *testing.T) {
	_, err := download.HashFile("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAssignPort_ReusesReleasedPorts(t *testing.T) {
	cfg := config.Config{BasePort: 5000}
	m := NewManager(cfg)

	m.mu.Lock()
	p1 := m.assignPort()
	p2 := m.assignPort()
	m.releasePort(p1)
	p3 := m.assignPort()
	m.mu.Unlock()

	if p1 != 5000 {
		t.Errorf("first port = %d, want 5000", p1)
	}
	if p2 != 5001 {
		t.Errorf("second port = %d, want 5001", p2)
	}
	if p3 != 5000 {
		t.Errorf("released port should be reused; got %d, want 5000", p3)
	}
}

func TestAssignPort_SkipsVersiondListenPort(t *testing.T) {
	m := NewManager(config.Config{BasePort: 8079})

	m.mu.Lock()
	p1 := m.assignPort()
	p2 := m.assignPort()
	m.mu.Unlock()

	if p1 != 8079 {
		t.Errorf("first port = %d, want 8079", p1)
	}
	if p2 != 8081 {
		t.Errorf("second port = %d, want 8081", p2)
	}
}

func TestAssignPort_NormalizesOutOfRangeBasePort(t *testing.T) {
	m := NewManager(config.Config{BasePort: 70000})

	m.mu.Lock()
	port := m.assignPort()
	m.mu.Unlock()

	if port != 5000 {
		t.Errorf("port = %d, want 5000", port)
	}
}

func TestAtomicCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	content := []byte("binary content")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	if err := atomicCopy(src, dst); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("copied content = %q, want %q", got, content)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0755 != 0755 {
		t.Errorf("mode = %o, want 0755", info.Mode())
	}
}

func TestInstallBinPathUsesVersionAndSHA(t *testing.T) {
	m := NewManager(config.Config{BinDir: "/opt/versiond/bin", BinaryName: "devshardd", BasePort: 5000})
	got := m.installBinPath("v2", "abc123")
	want := filepath.Join("/opt/versiond/bin", "v2", "abc123", "devshardd")
	if got != want {
		t.Fatalf("installBinPath = %q, want %q", got, want)
	}
}

func TestRollingOverlapAllowedRequiresPostgresForDevshard(t *testing.T) {
	t.Setenv("PGHOST", "")
	dataDir := t.TempDir()
	devshardMgr := NewManager(config.Config{DataDir: dataDir, BinaryName: "devshard", BasePort: 5000})
	if devshardMgr.rollingOverlapAllowed("v1") {
		t.Fatal("devshard overlap should require PGHOST")
	}

	t.Setenv("PGHOST", "postgres")
	if !devshardMgr.rollingOverlapAllowed("v1") {
		t.Fatal("devshard overlap should be allowed when PGHOST is set and sqlite has no sessions")
	}

	writeDevshardMetaDB(t, filepath.Join(dataDir, "v1"), 1)
	if devshardMgr.rollingOverlapAllowed("v1") {
		t.Fatal("devshard overlap should be disabled while sqlite sessions are present")
	}

	t.Setenv("PGHOST", "")
	testappMgr := NewManager(config.Config{BinaryName: "testapp", BasePort: 5000})
	if !testappMgr.rollingOverlapAllowed("v1") {
		t.Fatal("non-devshard test binary should allow overlap without PGHOST")
	}
}

func TestChildStopTimeoutHonorsDevshardShutdownGrace(t *testing.T) {
	t.Setenv("DEVSHARD_SHUTDOWN_GRACE", "")
	devshardMgr := NewManager(config.Config{BinaryName: "devshardd", DrainKillGrace: 30 * time.Second})
	if got := devshardMgr.childStopTimeout(); got != defaultDevshardShutdownGrace {
		t.Fatalf("childStopTimeout = %s, want default devshard grace", got)
	}

	t.Setenv("DEVSHARD_SHUTDOWN_GRACE", "2m")
	devshardMgr = NewManager(config.Config{BinaryName: "devshardd", DrainKillGrace: 30 * time.Second})
	if got := devshardMgr.childStopTimeout(); got != 2*time.Minute {
		t.Fatalf("childStopTimeout = %s, want DEVSHARD_SHUTDOWN_GRACE", got)
	}
	if got := devshardMgr.ShutdownTimeout(); got != 2*time.Minute+managerShutdownOverhead {
		t.Fatalf("ShutdownTimeout = %s, want DEVSHARD_SHUTDOWN_GRACE plus overhead", got)
	}

	t.Setenv("DEVSHARD_SHUTDOWN_GRACE", "5s")
	devshardMgr = NewManager(config.Config{BinaryName: "devshardd", DrainKillGrace: 30 * time.Second})
	if got := devshardMgr.childStopTimeout(); got != 30*time.Second {
		t.Fatalf("childStopTimeout = %s, want VERSIOND_DRAIN_KILL_GRACE", got)
	}

	testappMgr := NewManager(config.Config{BinaryName: "testapp", DrainKillGrace: 30 * time.Second})
	if got := testappMgr.childStopTimeout(); got != 30*time.Second {
		t.Fatalf("testapp childStopTimeout = %s, want drain kill grace", got)
	}
}

func TestGCInstalledVersionDirsRemovesOldCompleteInstallsOnly(t *testing.T) {
	binDir := t.TempDir()
	binaryName := "devshardd"
	baseTime := time.Now().Add(-time.Hour)

	writeInstalledVersion(t, binDir, "v1", "protected", binaryName, baseTime)
	writeInstalledVersion(t, binDir, "v1", "stale-newest", binaryName, baseTime.Add(4*time.Minute))
	writeInstalledVersion(t, binDir, "v1", "stale-middle", binaryName, baseTime.Add(3*time.Minute))
	writeInstalledVersion(t, binDir, "v1", "stale-old", binaryName, baseTime.Add(2*time.Minute))
	incompleteDir := filepath.Join(binDir, "v1", "incomplete")
	if err := os.MkdirAll(incompleteDir, 0o755); err != nil {
		t.Fatal(err)
	}

	keep := map[string]map[string]struct{}{
		"v1": {"protected": {}},
	}
	gcInstalledVersionDirs(binDir, binaryName, keep, 2)

	assertPathExists(t, filepath.Join(binDir, "v1", "protected"))
	assertPathExists(t, filepath.Join(binDir, "v1", "stale-newest"))
	assertPathExists(t, filepath.Join(binDir, "v1", "stale-middle"))
	assertPathExists(t, incompleteDir)
	assertPathMissing(t, filepath.Join(binDir, "v1", "stale-old"))
}

func TestReconcile_OverrideStartsChild(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	dataDir := filepath.Join(dir, "data")

	// Create a fake override binary.
	overrideBin := filepath.Join(dir, "override-binary")
	if err := os.WriteFile(overrideBin, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		BinDir:     binDir,
		DataDir:    dataDir,
		BinaryName: "devshard",
		BasePort:   5000,
		Overrides:  map[string]string{"v1": overrideBin},
	}
	m := NewManager(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	desired := []oracle.Version{{Name: "v1"}}
	if err := m.Reconcile(ctx, desired); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, running := m.processes["v1"]
	m.mu.Unlock()

	if !running {
		t.Error("override version v1 should be running")
	}

	// Verify the binary was copied (not symlinked).
	binPath := filepath.Join(binDir, "v1", "devshard")
	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(overrideBin)
	if string(got) != string(want) {
		t.Error("override binary was not copied correctly")
	}

	cancel()
	m.Shutdown(context.Background())
}

func TestReconcile_ForceVersionsInjectIntoDesired(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	dataDir := filepath.Join(dir, "data")

	overrideBin := filepath.Join(dir, "override-binary")
	if err := os.WriteFile(overrideBin, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		BinDir:        binDir,
		DataDir:       dataDir,
		BinaryName:    "devshard",
		BasePort:      5000,
		Overrides:     map[string]string{"forced1": overrideBin},
		ForceVersions: []string{"forced1"},
	}
	m := NewManager(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Oracle returns no versions, but forced1 should still be started.
	if err := m.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, running := m.processes["forced1"]
	m.mu.Unlock()

	if !running {
		t.Error("forced version forced1 should be running")
	}

	cancel()
	m.Shutdown(context.Background())
}

func TestReconcile_ForceWithoutOverrideSkipped(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		BinDir:        filepath.Join(dir, "bin"),
		DataDir:       filepath.Join(dir, "data"),
		BinaryName:    "devshard",
		BasePort:      5000,
		Overrides:     map[string]string{},
		ForceVersions: []string{"nooverride"},
	}
	m := NewManager(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should not error, just skip the forced version.
	if err := m.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, running := m.processes["nooverride"]
	m.mu.Unlock()

	if running {
		t.Error("forced version without override should not be running")
	}
}

func TestRunChild_RemovesFromProcessesOnStartFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		BinDir:     filepath.Join(dir, "bin"),
		DataDir:    filepath.Join(dir, "data"),
		BinaryName: "nonexistent",
		BasePort:   5000,
	}
	m := NewManager(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := oracle.Version{Name: "v1"}

	m.mu.Lock()
	m.startChild(ctx, v, "missing", filepath.Join(dir, "missing"), true)
	c := m.processes["v1"]
	m.mu.Unlock()

	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("runChild did not exit after start failure")
	}

	m.mu.Lock()
	_, stillInMap := m.processes["v1"]
	m.mu.Unlock()

	if stillInMap {
		t.Error("child should be removed from processes after start failure")
	}
}

func TestWaitForRestartBackoffRechecksRestartAfterSleep(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	c := &child{restart: true}

	m.mu.Lock()
	c.restart = false
	m.mu.Unlock()

	if m.waitForRestartBackoff(context.Background(), c, time.Millisecond) {
		t.Fatal("restart disabled during backoff should stop restart loop")
	}
}

func TestReconcile_DrainsRemovedVersionsAsync(t *testing.T) {
	dir := t.TempDir()
	var drainHits atomic.Int32
	var statusHits atomic.Int32
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/drain":
			drainHits.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/drain/status":
			statusHits.Add(1)
			json.NewEncoder(w).Encode(map[string]int64{"inflight": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer shutdown()

	cfg := config.Config{
		BinDir:            filepath.Join(dir, "bin"),
		DataDir:           filepath.Join(dir, "data"),
		BinaryName:        "devshard",
		BasePort:          5000,
		DrainTimeout:      time.Hour,
		DrainPollInterval: time.Hour,
		DrainKillGrace:    50 * time.Millisecond,
		Overrides:         map[string]string{},
	}
	m := NewManager(cfg)

	done := make(chan struct{})
	cancelled := make(chan struct{})
	var cancelOnce sync.Once

	m.mu.Lock()
	m.processes["old"] = &child{
		version:       oracle.Version{Name: "old"},
		archiveSHA256: "old-sha",
		port:          port,
		cancel: func() {
			cancelOnce.Do(func() {
				close(cancelled)
				close(done)
			})
		},
		done:    done,
		status:  statusRunning,
		restart: true,
	}
	m.rebuildRoutes()
	m.mu.Unlock()

	ctx := context.Background()
	start := time.Now()
	if err := m.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Reconcile blocked for %s while removed child was still draining", elapsed)
	}
	if drainHits.Load() != 1 {
		t.Fatalf("drain hits = %d, want 1", drainHits.Load())
	}
	select {
	case <-cancelled:
		t.Fatal("removed child should not be cancelled while drain status reports in-flight work")
	default:
	}

	m.mu.Lock()
	_, stillRunning := m.processes["old"]
	draining := m.draining["old"]
	var removed *child
	if len(draining) == 1 {
		removed = draining[0]
	}
	routes := m.RouteTable().Load().(map[string]string)
	m.mu.Unlock()

	if stillRunning {
		t.Error("removed version should no longer be in processes")
	}
	if removed == nil {
		t.Fatal("removed version should be tracked as draining")
	}
	if removed.status != statusDraining {
		t.Fatalf("removed status = %q, want %q", removed.status, statusDraining)
	}
	if removed.restart {
		t.Fatal("removed draining child should not restart")
	}
	if _, routed := routes["old"]; routed {
		t.Fatal("removed version should not remain routed")
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for statusHits.Load() == 0 && time.Now().Before(deadline) {
		select {
		case <-cancelled:
			t.Fatal("removed child should not be cancelled while drain status reports in-flight work")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if statusHits.Load() == 0 {
		t.Fatal("expected drain status to be polled")
	}

	removed.cancel()
	waitForChild(removed, time.Second)
}

func TestReconcile_RemovedLegacyVersionUsesDrainGraceBeforeCancel(t *testing.T) {
	dir := t.TempDir()
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer shutdown()

	m := NewManager(config.Config{
		BinDir:            filepath.Join(dir, "bin"),
		DataDir:           filepath.Join(dir, "data"),
		BinaryName:        "devshard",
		BasePort:          5000,
		DrainTimeout:      time.Hour,
		DrainPollInterval: time.Hour,
		DrainKillGrace:    50 * time.Millisecond,
	})

	done := make(chan struct{})
	cancelled := make(chan struct{})
	var cancelOnce sync.Once

	m.mu.Lock()
	m.processes["legacy"] = &child{
		version: oracle.Version{Name: "legacy"},
		port:    port,
		cancel: func() {
			cancelOnce.Do(func() {
				close(cancelled)
				close(done)
			})
		},
		done:    done,
		status:  statusRunning,
		restart: true,
	}
	m.rebuildRoutes()
	m.mu.Unlock()

	start := time.Now()
	if err := m.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatal("legacy child should get drain grace before SIGTERM")
	default:
	}

	select {
	case <-cancelled:
		if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
			t.Fatalf("legacy child cancelled after %s, want drain grace first", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy child was not cancelled after drain grace")
	}
}

func TestInstalledVersionMatches_DetectsBinaryHashMismatch(t *testing.T) {
	versionDir := t.TempDir()
	binPath := filepath.Join(versionDir, "devshard")
	archiveHash := sha256Hex([]byte("archive"))
	originalBinary := []byte("#!/bin/sh\nsleep 30\n")
	tamperedBinary := []byte("#!/bin/sh\necho tampered\n")

	if err := os.WriteFile(binPath, originalBinary, 0755); err != nil {
		t.Fatal(err)
	}
	writeInstallMetadataFile(t, versionDir, download.InstallMetadata{
		ArchiveSHA256: archiveHash,
		BinarySHA256:  sha256Hex(originalBinary),
	})

	if err := os.WriteFile(binPath, tamperedBinary, 0755); err != nil {
		t.Fatal(err)
	}

	matches, metadata, diskBinaryHash, err := installedVersionMatches(versionDir, binPath, archiveHash)
	if err != nil {
		t.Fatal(err)
	}
	if matches {
		t.Fatal("expected tampered binary to fail installedVersionMatches")
	}
	if metadata.BinarySHA256 != sha256Hex(originalBinary) {
		t.Errorf("recorded binary hash = %q, want %q", metadata.BinarySHA256, sha256Hex(originalBinary))
	}
	if diskBinaryHash != sha256Hex(tamperedBinary) {
		t.Errorf("disk binary hash = %q, want %q", diskBinaryHash, sha256Hex(tamperedBinary))
	}
}

func TestWaitForReadyFallsBackToHealthzWhenDefaultReadyMissing(t *testing.T) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer shutdown()

	if !waitForReady(context.Background(), port, "/ready", time.Second) {
		t.Fatal("expected /ready 404 to fall back to /healthz")
	}
}

func TestWaitForReadyDoesNotFallbackForCustomReadyPath(t *testing.T) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer shutdown()

	if waitForReady(context.Background(), port, "/custom-ready", 200*time.Millisecond) {
		t.Fatal("custom ready path should not use legacy fallback")
	}
}

func TestDrainAndStop_LegacyDrainStatusUsesShortGrace(t *testing.T) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer shutdown()

	m := NewManager(config.Config{
		BasePort:          5000,
		DrainTimeout:      time.Hour,
		DrainPollInterval: time.Hour,
		DrainKillGrace:    20 * time.Millisecond,
	})
	done := make(chan struct{})
	cancelled := false
	c := &child{
		version: oracle.Version{Name: "v1"},
		port:    port,
		done:    done,
		cancel: func() {
			cancelled = true
			close(done)
		},
	}

	start := time.Now()
	m.drainAndStop(c)
	if !cancelled {
		t.Fatal("legacy child should be cancelled after short drain grace")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("legacy drain took %s, want short grace instead of full timeout", elapsed)
	}
}

func TestLifecycleRequestsUseAdminPortWhenAvailable(t *testing.T) {
	var publicHits atomic.Int32
	publicPort, publicShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicHits.Add(1)
		http.Error(w, "public endpoint should not receive lifecycle traffic", http.StatusInternalServerError)
	}))
	defer publicShutdown()

	var drainHits atomic.Int32
	var statusHits atomic.Int32
	adminPort, adminShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/drain":
			drainHits.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/drain/status":
			statusHits.Add(1)
			json.NewEncoder(w).Encode(map[string]int64{"inflight": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer adminShutdown()

	m := NewManager(config.Config{
		BasePort:        5000,
		DrainPath:       "/drain",
		DrainStatusPath: "/drain/status",
	})
	c := &child{
		version:   oracle.Version{Name: "v1"},
		port:      publicPort,
		adminPort: adminPort,
	}

	m.requestDrain(c)
	inflight, err := m.fetchInflight(c)
	if err != nil {
		t.Fatal(err)
	}
	if inflight != 0 {
		t.Fatalf("inflight = %d, want 0", inflight)
	}
	if drainHits.Load() != 1 {
		t.Fatalf("admin drain hits = %d, want 1", drainHits.Load())
	}
	if statusHits.Load() != 1 {
		t.Fatalf("admin status hits = %d, want 1", statusHits.Load())
	}
	if publicHits.Load() != 0 {
		t.Fatalf("public lifecycle hits = %d, want 0", publicHits.Load())
	}
}

func TestDrainAndStop_WaitsForIdleBeforeCancel(t *testing.T) {
	var statusRequests int32
	var cancelled atomic.Bool
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&statusRequests, 1)
		if count == 1 {
			if cancelled.Load() {
				t.Error("child cancelled before idle drain status")
			}
			json.NewEncoder(w).Encode(map[string]int64{"inflight": 1})
			return
		}
		json.NewEncoder(w).Encode(map[string]int64{"inflight": 0})
	}))
	defer shutdown()

	m := NewManager(config.Config{
		BasePort:          5000,
		DrainTimeout:      time.Second,
		DrainPollInterval: 5 * time.Millisecond,
		DrainKillGrace:    50 * time.Millisecond,
	})
	done := make(chan struct{})
	c := &child{
		version: oracle.Version{Name: "v1"},
		port:    port,
		done:    done,
		cancel: func() {
			cancelled.Store(true)
			close(done)
		},
	}

	m.drainAndStop(c)
	if !cancelled.Load() {
		t.Fatal("idle child should be cancelled after drain")
	}
	if got := atomic.LoadInt32(&statusRequests); got < 2 {
		t.Fatalf("drain status requests = %d, want at least 2", got)
	}
}

func TestDrainAndStop_TimesOutWithInflightWork(t *testing.T) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int64{"inflight": 1})
	}))
	defer shutdown()

	m := NewManager(config.Config{
		BasePort:          5000,
		DrainTimeout:      20 * time.Millisecond,
		DrainPollInterval: 5 * time.Millisecond,
		DrainKillGrace:    50 * time.Millisecond,
	})
	done := make(chan struct{})
	cancelled := false
	c := &child{
		version: oracle.Version{Name: "v1"},
		port:    port,
		done:    done,
		cancel: func() {
			cancelled = true
			close(done)
		},
	}

	start := time.Now()
	m.drainAndStop(c)
	if !cancelled {
		t.Fatal("child should be cancelled after drain timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain timeout path took %s", elapsed)
	}
}

func TestDownloadAndSwap_NewChildNotReadyKeepsOldServing(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	dataDir := filepath.Join(dir, "data")
	newBinary := []byte(`#!/bin/sh
case "$1" in
--print-binary-version) echo "testapp-new" ;;
--print-protocol-version) echo "v1" ;;
*) exec sleep 60 ;;
esac
`)
	zipData, archiveHash := zipBinary(t, "testapp", newBinary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipData)
	}))
	defer srv.Close()

	m := NewManager(config.Config{
		BinDir:         binDir,
		DataDir:        dataDir,
		BinaryName:     "testapp",
		BasePort:       6200,
		ReadyTimeout:   20 * time.Millisecond,
		DrainKillGrace: 100 * time.Millisecond,
	})
	old := &child{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: sha256Hex([]byte("old-archive")),
		port:          9001,
		done:          make(chan struct{}),
		status:        statusRunning,
		restart:       true,
		cancel: func() {
			t.Fatal("old child should not be cancelled when replacement is not ready")
		},
	}

	m.mu.Lock()
	m.processes["v1"] = old
	m.downloading["v1"] = struct{}{}
	m.rebuildRoutes()
	m.mu.Unlock()

	err := m.downloadAndSwap(context.Background(), oracle.Version{
		Name:   "v1",
		Binary: srv.URL,
		SHA256: archiveHash,
	}, archiveHash, old)
	if err == nil {
		t.Fatal("expected swap error when new child is not ready")
	}

	m.mu.Lock()
	current := m.processes["v1"]
	_, downloading := m.downloading["v1"]
	draining := len(m.draining["v1"])
	m.mu.Unlock()
	if current != old {
		t.Fatalf("current child changed after aborted swap")
	}
	if old.status != statusRunning || !old.restart {
		t.Fatalf("old child status/restart = %s/%v, want running/true", old.status, old.restart)
	}
	if downloading {
		t.Fatal("downloading marker should be cleared after aborted swap")
	}
	if draining != 0 {
		t.Fatalf("draining children = %d, want 0", draining)
	}
	routes := m.RouteTable().Load().(map[string]string)
	if routes["v1"] != "localhost:9001" {
		t.Fatalf("route = %q, want old child route", routes["v1"])
	}
}

func TestReconcile_DownloadedVersionDoesNotRedownloadWhenInstallStateMatches(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	dataDir := filepath.Join(dir, "data")
	archiveHash := sha256Hex([]byte("archive-v1"))
	versionDir := filepath.Join(binDir, "v1", archiveHash)
	binPath := filepath.Join(versionDir, "devshard")
	binaryContent := []byte("#!/bin/sh\nsleep 30\n")

	if err := os.MkdirAll(versionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, binaryContent, 0755); err != nil {
		t.Fatal(err)
	}
	writeInstallMetadataFile(t, versionDir, download.InstallMetadata{
		ArchiveSHA256: archiveHash,
		BinarySHA256:  sha256Hex(binaryContent),
	})

	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.Config{
		BinDir:     binDir,
		DataDir:    dataDir,
		BinaryName: "devshard",
		BasePort:   5000,
	}
	m := NewManager(cfg)

	done := make(chan struct{})
	close(done)
	cancelled := false

	m.mu.Lock()
	m.processes["v1"] = &child{
		version:       oracle.Version{Name: "v1", Binary: srv.URL, SHA256: archiveHash},
		archiveSHA256: archiveHash,
		binPath:       binPath,
		port:          5000,
		cancel:        func() { cancelled = true },
		done:          done,
		status:        statusRunning,
	}
	m.mu.Unlock()

	if err := m.Reconcile(context.Background(), []oracle.Version{{
		Name:   "v1",
		Binary: srv.URL,
		SHA256: archiveHash,
	}}); err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("unexpected download requests: got %d, want 0", got)
	}
	if cancelled {
		t.Fatal("running version should not be swapped when install state matches")
	}

	m.mu.Lock()
	_, downloading := m.downloading["v1"]
	m.mu.Unlock()
	if downloading {
		t.Fatal("version should not be marked as downloading when install state matches")
	}
}

func startLocalHTTPServer(t *testing.T, handler http.Handler) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() {
		_ = srv.Serve(ln)
	}()
	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return ln.Addr().(*net.TCPAddr).Port, shutdown
}

func writeDevshardMetaDB(t *testing.T, dataDir string, rows int) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, devshardMetaDBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE escrow_epoch (escrow_id TEXT PRIMARY KEY, epoch_id INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec(`INSERT INTO escrow_epoch (escrow_id, epoch_id) VALUES (?, ?)`, "escrow-"+string(rune('a'+i)), i+1); err != nil {
			t.Fatal(err)
		}
	}
}

func writeInstalledVersion(t *testing.T, binDir, versionName, sha, binaryName string, modTime time.Time) {
	t.Helper()
	versionDir := filepath.Join(binDir, versionName, sha)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(versionDir, binaryName)
	if err := os.WriteFile(binPath, []byte("binary-"+sha), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstallMetadataFile(t, versionDir, download.InstallMetadata{
		ArchiveSHA256: sha,
		BinarySHA256:  "binary-" + sha,
	})
	if err := os.Chtimes(filepath.Join(versionDir, download.InstallMetadataFilename), modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path to exist %s: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path to be missing %s, stat err %v", path, err)
	}
}

func writeInstallMetadataFile(t *testing.T, versionDir string, metadata download.InstallMetadata) {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, download.InstallMetadataFilename), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func zipBinary(t *testing.T, binaryName string, data []byte) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(binaryName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zipData := buf.Bytes()
	return zipData, sha256Hex(zipData)
}

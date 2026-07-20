package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/download"
	"versioned/internal/oracle"
)

func TestChildEnvIncludesVersionLogPrefix(t *testing.T) {
	env := childEnv("0.2.13-v2-r2")
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
	env := childEnv("v2")
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

func TestPreflightChild_MissingBinary(t *testing.T) {
	_, err := preflightChild(filepath.Join(t.TempDir(), "missing"), "v2")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestPreflightChild_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "legacy-bin")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
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
		t.Fatalf("binaryLogVersion = %q, want stamped build id", preflight.binaryLogVersion)
	}
}

func TestPreflightChild_BinaryFlagUnsupported(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "protocol-only-stamp")
	script := `#!/bin/sh
case "$1" in
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

func TestAssignPort_Stable(t *testing.T) {
	cfg := config.Config{BasePort: 5000}
	m := NewManager(cfg)

	m.mu.Lock()
	p1 := m.assignPort("v1")
	p2 := m.assignPort("v2")
	p1again := m.assignPort("v1")
	m.mu.Unlock()

	if p1 != 5000 {
		t.Errorf("first port = %d, want 5000", p1)
	}
	if p2 != 5001 {
		t.Errorf("second port = %d, want 5001", p2)
	}
	if p1again != p1 {
		t.Errorf("repeated assignPort gave %d, want %d", p1again, p1)
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
	m.startChild(ctx, v)
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

func TestReconcile_StopsRemovedVersions(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		BinDir:     filepath.Join(dir, "bin"),
		DataDir:    filepath.Join(dir, "data"),
		BinaryName: "devshard",
		BasePort:   5000,
		Overrides:  map[string]string{},
	}
	m := NewManager(cfg)

	done := make(chan struct{})
	close(done)

	m.mu.Lock()
	cancelled := false
	m.processes["old"] = &child{
		version: oracle.Version{Name: "old"},
		port:    5000,
		cancel:  func() { cancelled = true },
		done:    done,
		status:  statusRunning,
	}
	m.mu.Unlock()

	ctx := context.Background()
	// Reconcile with empty desired list should stop "old".
	if err := m.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}

	if !cancelled {
		t.Error("removed version should have been cancelled")
	}

	m.mu.Lock()
	_, stillRunning := m.processes["old"]
	m.mu.Unlock()

	if stillRunning {
		t.Error("removed version should no longer be in processes")
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

func TestReconcile_DownloadedVersionDoesNotRedownloadWhenInstallStateMatches(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	dataDir := filepath.Join(dir, "data")
	versionDir := filepath.Join(binDir, "v1")
	binPath := filepath.Join(versionDir, "devshard")
	archiveHash := sha256Hex([]byte("archive-v1"))
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
		version: oracle.Version{Name: "v1", Binary: srv.URL, SHA256: archiveHash},
		port:    5000,
		cancel:  func() { cancelled = true },
		done:    done,
		status:  statusRunning,
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

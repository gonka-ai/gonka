package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/oracle"
)

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

	got, err := hashFile(path)
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
	_, err := hashFile("/nonexistent/path")
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
		BinaryName: "subnet",
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
	binPath := filepath.Join(binDir, "v1", "subnet")
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
		BinaryName:    "subnet",
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
		BinaryName:    "subnet",
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
		BinaryName: "subnet",
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

package process

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"versioned/internal/config"
	"versioned/internal/download"
	"versioned/internal/oracle"
	"versioned/internal/proxy"
)

func TestChildEnvIncludesVersionLogPrefix(t *testing.T) {
	env := childEnv("0.2.13-v2-r2", "", nil)
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
	env := childEnv("v2", "", nil)
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
	env := childEnv("v2", "127.0.0.1:6001", nil)
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

	env := childEnv("v2", "", nil)
	for _, entry := range env {
		if strings.HasPrefix(entry, "DEVSHARD_ADMIN_ADDR=") {
			t.Fatalf("childEnv leaked parent %q", entry)
		}
	}
}

func TestChildEnvOverridesParentHADeclaration(t *testing.T) {
	t.Setenv(envHADeployment, "true")
	ha := false

	env := childEnv("v2", "", &ha)
	values := make([]string, 0, 1)
	for _, entry := range env {
		if strings.HasPrefix(entry, envHADeployment+"=") {
			values = append(values, entry)
		}
	}
	if len(values) != 1 || values[0] != envHADeployment+"=false" {
		t.Fatalf("child HA environment = %v, want one canonical false value", values)
	}
}

func TestParseHADeployment(t *testing.T) {
	tests := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: ""},
		{raw: "0"},
		{raw: "f"},
		{raw: "false"},
		{raw: "no"},
		{raw: "off"},
		{raw: "1", want: true},
		{raw: "t", want: true},
		{raw: "true", want: true},
		{raw: "yes", want: true},
		{raw: "on", want: true},
		{raw: " ON ", want: true},
		{raw: " OFF ", want: false},
		{raw: "enabled", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseHADeployment(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected invalid boolean error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("parseHADeployment(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestPreflightChild_MissingBinary(t *testing.T) {
	_, err := preflightChildWithAdminProbeContext(
		context.Background(),
		filepath.Join(t.TempDir(), "missing"),
		"v2",
		false,
	)
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

	preflight, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.binaryLogVersion != "v2" {
		t.Fatalf("legacy preflight binaryLogVersion = %q, want %q", preflight.binaryLogVersion, "v2")
	}
}

func TestPostgresSchemaInitializationPrecedesLegacyChildren(t *testing.T) {
	t.Setenv(envHADeployment, "true")
	t.Setenv("PGHOST", "postgres")
	dir := t.TempDir()
	currentBin := filepath.Join(dir, "current")
	legacyBin := filepath.Join(dir, "legacy")
	if err := os.WriteFile(currentBin, []byte(`#!/bin/sh
case "$1" in
--initialize-postgres-schema) exit 0 ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyBin, []byte(`#!/bin/sh
echo "unknown flag: $1" >&2
exit 2
`), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{BinaryName: "devshardd"})
	m.configureStorageInitCandidates(map[storageInitCandidate]struct{}{
		{version: "legacy", artifact: "legacy-sha"}:   {},
		{version: "current", artifact: "current-sha"}: {},
	})
	legacyDone := make(chan error, 1)
	go func() {
		legacyDone <- m.ensurePostgresSchemaInitialized(context.Background(), &child{
			version:       oracle.Version{Name: "legacy"},
			archiveSHA256: "legacy-sha",
			binPath:       legacyBin,
		}, childPreflight{binaryLogVersion: "legacy", haDeployment: boolPointer(true)})
	}()

	select {
	case err := <-legacyDone:
		t.Fatalf("legacy initialization barrier returned before a current child initialized the schema: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	err := m.ensurePostgresSchemaInitialized(context.Background(), &child{
		version:       oracle.Version{Name: "current"},
		archiveSHA256: "current-sha",
		binPath:       currentBin,
	}, childPreflight{binaryLogVersion: "current", haDeployment: boolPointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-legacyDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy child did not pass the barrier after schema initialization")
	}
}

func TestPostgresSchemaInitializationAllowsLegacyOnlyCatalog(t *testing.T) {
	t.Setenv(envHADeployment, "true")
	t.Setenv("PGHOST", "postgres")
	dir := t.TempDir()
	legacyBin := filepath.Join(dir, "legacy")
	if err := os.WriteFile(legacyBin, []byte(`#!/bin/sh
echo "unknown flag: $1" >&2
exit 2
`), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{BinaryName: "devshardd"})
	m.configureStorageInitCandidates(map[storageInitCandidate]struct{}{
		{version: "legacy-a", artifact: "legacy-a-sha"}: {},
		{version: "legacy-b", artifact: "legacy-b-sha"}: {},
	})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- m.ensurePostgresSchemaInitialized(context.Background(), &child{
			version:       oracle.Version{Name: "legacy-a"},
			archiveSHA256: "legacy-a-sha",
			binPath:       legacyBin,
		}, childPreflight{binaryLogVersion: "legacy-a", haDeployment: boolPointer(true)})
	}()
	select {
	case err := <-firstDone:
		t.Fatalf("first legacy child passed before every desired binary was classified: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := m.ensurePostgresSchemaInitialized(ctx, &child{
		version:       oracle.Version{Name: "legacy-b"},
		archiveSHA256: "legacy-b-sha",
		binPath:       legacyBin,
	}, childPreflight{binaryLogVersion: "legacy-b", haDeployment: boolPointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy-only catalog did not start after every desired binary was classified")
	}
}

func TestPostgresSchemaInitializationDoesNotBlockNonHAChild(t *testing.T) {
	t.Setenv(envHADeployment, "true")
	t.Setenv("PGHOST", "unavailable-postgres")
	dir := t.TempDir()
	marker := filepath.Join(dir, "initializer-called")
	t.Setenv("INIT_MARKER", marker)
	legacyBin := filepath.Join(dir, "legacy")
	if err := os.WriteFile(legacyBin, []byte(`#!/bin/sh
touch "$INIT_MARKER"
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{BinaryName: "devshardd"})
	err := m.ensurePostgresSchemaInitialized(context.Background(), &child{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: "legacy-sha",
		binPath:       legacyBin,
	}, childPreflight{binaryLogVersion: "v1", haDeployment: boolPointer(false)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("non-HA child invoked PostgreSQL initializer: %v", err)
	}
}

func TestPostgresChildStorageConfigured(t *testing.T) {
	for _, key := range []string{"PGHOST", "PGSERVICE"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("PGHOST", "")
			t.Setenv("PGSERVICE", "")
			t.Setenv(key, "configured")
			if !postgresChildStorageConfigured() {
				t.Fatalf("%s did not select PostgreSQL child storage", key)
			}
		})
	}
}

func TestPostgresSchemaInitializationIgnoresStaleSHAClassification(t *testing.T) {
	t.Setenv(envHADeployment, "true")
	t.Setenv("PGHOST", "postgres")
	dir := t.TempDir()
	currentBin := filepath.Join(dir, "current")
	legacyBin := filepath.Join(dir, "legacy")
	entered := filepath.Join(dir, "entered")
	release := filepath.Join(dir, "release")
	t.Setenv("INIT_ENTERED", entered)
	t.Setenv("INIT_RELEASE", release)
	if err := os.WriteFile(currentBin, []byte(`#!/bin/sh
case "$1" in
--initialize-postgres-schema) exit 0 ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyBin, []byte(`#!/bin/sh
touch "$INIT_ENTERED"
while [ ! -e "$INIT_RELEASE" ]; do sleep 0.01; done
echo "unknown flag: $1" >&2
exit 2
`), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{BinaryName: "devshardd"})
	oldCandidate := storageInitCandidate{version: "v5", artifact: "old-sha"}
	newCandidate := storageInitCandidate{version: "v5", artifact: "new-sha"}
	m.configureStorageInitCandidates(map[storageInitCandidate]struct{}{oldCandidate: {}})
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- m.ensurePostgresSchemaInitialized(context.Background(), &child{
			version:       oracle.Version{Name: "v5"},
			archiveSHA256: "old-sha",
			binPath:       legacyBin,
		}, childPreflight{binaryLogVersion: "v5-old", haDeployment: boolPointer(true)})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old artifact did not enter initializer classification")
		}
		time.Sleep(10 * time.Millisecond)
	}

	m.configureStorageInitCandidates(map[storageInitCandidate]struct{}{newCandidate: {}})
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-oldDone:
		t.Fatalf("stale SHA closed the replacement barrier: %v", err)
	case <-m.storageInitDone:
		t.Fatal("stale SHA classified the replacement artifact as legacy")
	case <-time.After(100 * time.Millisecond):
	}

	err := m.ensurePostgresSchemaInitialized(context.Background(), &child{
		version:       oracle.Version{Name: "v5"},
		archiveSHA256: "new-sha",
		binPath:       currentBin,
	}, childPreflight{binaryLogVersion: "v5", haDeployment: boolPointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.storageInitDone:
	default:
		t.Fatal("replacement artifact did not complete PostgreSQL initialization")
	}
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale child did not leave the barrier after replacement initialization")
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

	preflight, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		false,
	)
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

	preflight, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		false,
	)
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

	_, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		false,
	)
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

	preflight, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		false,
	)
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

	preflight, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.adminAPISupported {
		t.Fatal("expected unsupported admin API flag to keep public lifecycle fallback")
	}
	if preflight.storageMode != "" {
		t.Fatalf("storageMode = %q, want legacy empty value", preflight.storageMode)
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
--print-storage-mode) echo "postgres" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	preflight, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !preflight.adminAPISupported {
		t.Fatal("expected admin API support to be detected")
	}
	if preflight.storageMode != "postgres" {
		t.Fatalf("storageMode = %q, want postgres", preflight.storageMode)
	}
}

func TestPreflightChild_StorageModeProbeErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "stamped-bin")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) echo "v2" ;;
--print-admin-api-version) echo "1" ;;
--print-storage-mode) echo "bad storage env" >&2; exit 1 ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		true,
	); err == nil {
		t.Fatal("expected storage mode probe error to fail preflight")
	}
}

func TestPreflightChild_HAStorageContract(t *testing.T) {
	tests := []struct {
		name          string
		ha            string
		nonHAVersions string
		storageMode   string
		legacy        bool
		wantHA        bool
		wantErr       bool
	}{
		{name: "postgres HA version", ha: "on", storageMode: "postgres", wantHA: true},
		{name: "non-postgres HA version", ha: "on", storageMode: "hybrid", wantErr: true},
		{name: "legacy binary in HA pool", ha: "on", legacy: true, wantErr: true},
		{name: "legacy-pinned binary", ha: "on", nonHAVersions: "v1, v2;v3", legacy: true},
		{name: "single-instance legacy binary", ha: "off", legacy: true},
		{name: "invalid HA declaration", ha: "enabled", storageMode: "postgres", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envHADeployment, tt.ha)
			t.Setenv(envNonHAVersions, tt.nonHAVersions)
			dir := t.TempDir()
			binPath := writeStorageModeProbeBinary(t, dir, "candidate", tt.storageMode)
			if tt.legacy {
				binPath = writeStorageModeLegacyBinary(t, dir, "candidate")
			}

			preflight, err := preflightChildWithAdminProbeContext(
				context.Background(),
				binPath,
				"v2",
				true,
			)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected HA storage preflight error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if preflight.haDeployment == nil {
				t.Fatal("devshard preflight did not set the child HA declaration")
			}
			if *preflight.haDeployment != tt.wantHA {
				t.Fatalf("child HA declaration = %v, want %v", *preflight.haDeployment, tt.wantHA)
			}
		})
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

	if _, err := preflightChildWithAdminProbeContext(
		context.Background(),
		binPath,
		"v2",
		false,
	); err == nil {
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
	routes := m.RouteTable().Load().(proxy.RouteTable)
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

	routes := m.RouteTable().Load().(proxy.RouteTable)
	if routes["v1"].Address() != "localhost:9001" {
		t.Errorf("v1 route = %q, want %q", routes["v1"].Address(), "localhost:9001")
	}
	if routes["v2"].Address() != "localhost:9002" {
		t.Errorf("v2 route = %q, want %q", routes["v2"].Address(), "localhost:9002")
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

	routes := m.RouteTable().Load().(proxy.RouteTable)
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

	routes := m.RouteTable().Load().(proxy.RouteTable)
	if routes["v1"].Address() != "localhost:9002" {
		t.Fatalf("route = %q, want new child", routes["v1"].Address())
	}

	statuses := m.Status()
	if len(statuses) != 2 {
		t.Fatalf("expected running + draining status, got %d", len(statuses))
	}
	var sawDraining bool
	for _, status := range statuses {
		if status.Status == "draining" && status.Port == 9001 && status.SHA256 == "old-sha" {
			sawDraining = true
		}
	}
	if !sawDraining {
		t.Fatalf("draining child not reported in status: %+v", statuses)
	}
}

func TestChildLifetimeIsIndependentFromReconcileContext(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	reconcileCtx, cancelReconcile := context.WithCancel(context.Background())
	m.mu.Lock()
	start, err := m.newChild(
		reconcileCtx,
		oracle.Version{Name: "v1"},
		"sha",
		"/tmp/testapp",
		true,
	)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	cancelReconcile()
	select {
	case <-start.ctx.Done():
		t.Fatal("cancelling reconcile context stopped the child lifetime")
	case <-time.After(20 * time.Millisecond):
	}

	start.child.Stop()
	select {
	case <-start.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("child stop did not cancel its lifetime context")
	}
}

func TestBeginHostDrainRejectsReconcileAndDisablesRestart(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	c := &child{
		version: oracle.Version{Name: "v1"},
		done:    make(chan struct{}),
		status:  statusRunning,
		restart: true,
	}
	m.mu.Lock()
	m.processes[c.version.Name] = c
	m.children[c] = struct{}{}
	m.mu.Unlock()

	m.BeginHostDrain()
	m.mu.Lock()
	hostDraining := m.hostDraining
	m.mu.Unlock()
	if !hostDraining {
		t.Fatal("manager should report host draining")
	}
	if c.restart {
		t.Fatal("host drain should disable child restart")
	}
	if err := m.Reconcile(context.Background(), nil); !errors.Is(err, ErrHostDraining) {
		t.Fatalf("Reconcile error = %v, want ErrHostDraining", err)
	}
}

func TestRequestChildrenDrainRemovesRouteBeforeLifecycleRequest(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000})
	var drainCalled atomic.Bool
	adminPort, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/drain" {
			http.NotFound(w, r)
			return
		}
		if _, ok := m.RouteTable().Load().(proxy.RouteTable)["v1"]; ok {
			t.Error("child route was still published during lifecycle drain request")
		}
		drainCalled.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer shutdown()

	c := &child{
		version:     oracle.Version{Name: "v1"},
		port:        9001,
		done:        make(chan struct{}),
		status:      statusRunning,
		restart:     true,
		proxyTarget: proxy.NewTarget("localhost:9001"),
	}
	setTestAdminPort(c, adminPort)
	m.mu.Lock()
	m.processes[c.version.Name] = c
	m.children[c] = struct{}{}
	m.rebuildRoutes()
	m.mu.Unlock()

	if err := m.RequestChildrenDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !drainCalled.Load() {
		t.Fatal("child drain endpoint was not called")
	}
	if c.status != statusRetiring {
		t.Fatalf("child status = %q, want retiring", c.status)
	}
	if c.restart {
		t.Fatal("draining child should not restart")
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
	p1 := mustAssignPort(t, m)
	p2 := mustAssignPort(t, m)
	m.releasePort(p1)
	p3 := mustAssignPort(t, m)
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
	p1 := mustAssignPort(t, m)
	p2 := mustAssignPort(t, m)
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
	port := mustAssignPort(t, m)
	m.mu.Unlock()

	if port != 5000 {
		t.Errorf("port = %d, want 5000", port)
	}
}

func TestAssignPort_ReturnsErrorWhenPoolExhausted(t *testing.T) {
	m := NewManager(config.Config{BasePort: maxChildPort})
	m.allocatedPorts[maxChildPort] = struct{}{}

	m.mu.Lock()
	port, err := m.assignPort()
	m.mu.Unlock()

	if port != 0 {
		t.Fatalf("port = %d, want zero value", port)
	}
	if !errors.Is(err, errChildPortPoolExhausted) {
		t.Fatalf("assignPort error = %v, want %v", err, errChildPortPoolExhausted)
	}
}

func TestStartChild_DoesNotPublishWhenPortPoolExhausted(t *testing.T) {
	m := NewManager(config.Config{BasePort: maxChildPort})
	m.allocatedPorts[maxChildPort] = struct{}{}
	v := oracle.Version{Name: "v1"}

	m.mu.Lock()
	err := m.startChild(context.Background(), v, "sha", "unused", true)
	_, published := m.processes[v.Name]
	m.mu.Unlock()

	if !errors.Is(err, errChildPortPoolExhausted) {
		t.Fatalf("startChild error = %v, want %v", err, errChildPortPoolExhausted)
	}
	if published {
		t.Fatal("child with no assigned port must not be published")
	}
}

func TestRunChild_ReleasesPublicPortWhenAdminPoolExhausted(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "devshardd")
	script := `#!/bin/sh
case "$1" in
--print-binary-version) echo "0.2.13-v2-r2" ;;
--print-protocol-version) echo "v2" ;;
--print-admin-api-version) echo "1" ;;
--print-storage-mode) echo "postgres" ;;
*) exit 99 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BinDir:     filepath.Join(dir, "bin"),
		DataDir:    filepath.Join(dir, "data"),
		BinaryName: "devshardd",
		BasePort:   maxChildPort,
	})
	v := oracle.Version{Name: "v2"}

	m.mu.Lock()
	if err := m.startChild(context.Background(), v, "sha", binPath, true); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	c := m.processes[v.Name]
	m.mu.Unlock()

	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not stop after admin port allocation failed")
	}

	m.mu.Lock()
	_, published := m.processes[v.Name]
	allocated := len(m.allocatedPorts)
	m.mu.Unlock()

	if published {
		t.Fatal("child with no admin port must not remain published")
	}
	if allocated != 0 {
		t.Fatalf("allocated ports = %d, want public port released", allocated)
	}
}

func mustAssignPort(t *testing.T, m *Manager) int {
	t.Helper()
	port, err := m.assignPort()
	if err != nil {
		t.Fatal(err)
	}
	return port
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
	devshardMgr := NewManager(config.Config{BinaryName: "devshard", BasePort: 5000})
	if devshardMgr.rollingOverlapAllowed("v1", &child{storageMode: ""}, "postgres") {
		t.Fatal("devshard overlap should be disabled when running child storage mode is unknown")
	}
	if devshardMgr.rollingOverlapAllowed("v1", &child{storageMode: "hybrid"}, "postgres") {
		t.Fatal("devshard overlap should be disabled when running child is not postgres-only")
	}
	if devshardMgr.rollingOverlapAllowed("v1", &child{storageMode: "postgres"}, "") {
		t.Fatal("devshard overlap should be disabled when new binary does not expose storage mode")
	}
	if devshardMgr.rollingOverlapAllowed("v1", &child{storageMode: "postgres"}, "hybrid") {
		t.Fatal("devshard overlap should be disabled when new binary is not postgres-only")
	}
	if !devshardMgr.rollingOverlapAllowed("v1", &child{storageMode: "postgres"}, "postgres") {
		t.Fatal("devshard overlap should be allowed when both children are postgres-only")
	}

	testappMgr := NewManager(config.Config{BinaryName: "testapp", BasePort: 5000})
	if !testappMgr.rollingOverlapAllowed("v1", &child{}, "") {
		t.Fatal("non-devshard test binary should allow overlap without storage mode probing")
	}
}

func TestChildStopTimeoutUsesEffectiveConfiguredGrace(t *testing.T) {
	devshardMgr := NewManager(config.Config{BinaryName: "devshardd", DrainKillGrace: 30 * time.Second})
	if got := devshardMgr.childStopTimeout(); got != config.DefaultDevshardShutdownGrace {
		t.Fatalf("childStopTimeout = %s, want default devshard grace", got)
	}

	devshardMgr = NewManager(config.Config{
		BinaryName:         "devshardd",
		DrainKillGrace:     30 * time.Second,
		ChildShutdownGrace: 2 * time.Minute,
	})
	if got := devshardMgr.childStopTimeout(); got != 2*time.Minute {
		t.Fatalf("childStopTimeout = %s, want configured child shutdown grace", got)
	}
	devshardMgr = NewManager(config.Config{
		BinaryName:         "devshardd",
		DrainKillGrace:     30 * time.Second,
		ChildShutdownGrace: 5 * time.Second,
	})
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
	if err := m.startChild(ctx, v, "missing", filepath.Join(dir, "missing"), true); err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
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
		stop: func() {
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
	drainDeadline := time.Now().Add(500 * time.Millisecond)
	for drainHits.Load() == 0 && time.Now().Before(drainDeadline) {
		time.Sleep(5 * time.Millisecond)
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
	routes := m.RouteTable().Load().(proxy.RouteTable)
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

	removed.Stop()
	waitForChild(removed)
}

func TestReconcile_ReaddedVersionWaitsForDrainingChild(t *testing.T) {
	dir := t.TempDir()
	binary := []byte(`#!/bin/sh
case "$1" in
--print-binary-version) echo "testapp-v1" ;;
--print-protocol-version) echo "v1" ;;
*) exec sleep 30 ;;
esac
`)
	zipData, archiveHash := zipBinary(t, "testapp", binary)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	m := NewManager(config.Config{
		BinDir:         filepath.Join(dir, "bin"),
		DataDir:        filepath.Join(dir, "data"),
		BinaryName:     "testapp",
		BasePort:       5000,
		DrainKillGrace: 100 * time.Millisecond,
	})
	m.draining["v1"] = []*child{{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: sha256Hex([]byte("removed-v1")),
		status:        statusDraining,
	}}
	desired := []oracle.Version{{Name: "v1", Binary: srv.URL, SHA256: archiveHash}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := m.Reconcile(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("download requests while previous child drains = %d, want 0", got)
	}
	m.mu.Lock()
	_, running := m.processes["v1"]
	_, downloading := m.downloading["v1"]
	m.mu.Unlock()
	if running || downloading {
		t.Fatalf("re-added version state while draining = running:%v downloading:%v, want false/false", running, downloading)
	}

	m.mu.Lock()
	delete(m.draining, "v1")
	m.mu.Unlock()
	if err := m.Reconcile(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("download requests after drain completed = %d, want 1", got)
	}
	m.mu.Lock()
	_, running = m.processes["v1"]
	m.mu.Unlock()
	if !running {
		t.Fatal("re-added version should start after previous child finishes draining")
	}

	cancel()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadAndStart_DoesNotOverlapDrainingChild(t *testing.T) {
	dir := t.TempDir()
	zipData, archiveHash := zipBinary(t, "testapp", []byte("test binary"))
	requestStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseDownload
		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	m := NewManager(config.Config{
		BinDir:     filepath.Join(dir, "bin"),
		DataDir:    filepath.Join(dir, "data"),
		BinaryName: "testapp",
		BasePort:   5000,
	})
	m.downloading["v1"] = struct{}{}
	v := oracle.Version{Name: "v1", Binary: srv.URL, SHA256: archiveHash}
	errCh := make(chan error, 1)
	go func() {
		errCh <- m.downloadAndStart(context.Background(), v, archiveHash)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("download did not start")
	}
	m.mu.Lock()
	m.draining["v1"] = []*child{{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: sha256Hex([]byte("removed-v1")),
		status:        statusDraining,
	}}
	m.mu.Unlock()
	close(releaseDownload)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	_, running := m.processes["v1"]
	_, downloading := m.downloading["v1"]
	m.mu.Unlock()
	if running {
		t.Fatal("download completion should not start a version while its previous child drains")
	}
	if downloading {
		t.Fatal("download marker should be cleared when start is deferred")
	}
}

func TestReconcile_ReaddedOverrideWaitsForDrainingChild(t *testing.T) {
	dir := t.TempDir()
	overrideBin := filepath.Join(dir, "override-binary")
	script := []byte(`#!/bin/sh
case "$1" in
--print-binary-version) echo "testapp-v1" ;;
--print-protocol-version) echo "v1" ;;
*) exec sleep 30 ;;
esac
`)
	if err := os.WriteFile(overrideBin, script, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BinDir:         filepath.Join(dir, "bin"),
		DataDir:        filepath.Join(dir, "data"),
		BinaryName:     "testapp",
		BasePort:       5000,
		DrainKillGrace: 100 * time.Millisecond,
		Overrides:      map[string]string{"v1": overrideBin},
	})
	m.draining["v1"] = []*child{{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: "override:removed",
		status:        statusDraining,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := m.Reconcile(ctx, []oracle.Version{{Name: "v1"}}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, running := m.processes["v1"]
	m.mu.Unlock()
	if running {
		t.Fatal("re-added override should wait for the previous child to finish draining")
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "v1", "testapp")); !os.IsNotExist(err) {
		t.Fatalf("override should not be copied while previous child drains, stat error: %v", err)
	}

	m.mu.Lock()
	delete(m.draining, "v1")
	m.mu.Unlock()
	if err := m.Reconcile(ctx, []oracle.Version{{Name: "v1"}}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, running = m.processes["v1"]
	m.mu.Unlock()
	if !running {
		t.Fatal("re-added override should start after previous child finishes draining")
	}

	cancel()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOverride_DoesNotReplaceChildMovedToDraining(t *testing.T) {
	dir := t.TempDir()
	overrideSrc := filepath.Join(dir, "override-source")
	binPath := filepath.Join(dir, "bin", "v1", "testapp")
	if err := os.WriteFile(overrideSrc, []byte("new override"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("old override"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BinDir:         filepath.Join(dir, "bin"),
		DataDir:        filepath.Join(dir, "data"),
		BinaryName:     "testapp",
		BasePort:       5000,
		DrainKillGrace: 100 * time.Millisecond,
	})
	done := make(chan struct{})
	var cancelOnce sync.Once
	existing := &child{
		version: oracle.Version{Name: "v1"},
		binPath: binPath,
		done:    done,
		status:  statusRunning,
		restart: true,
	}
	existing.stop = func() {
		cancelOnce.Do(func() {
			m.mu.Lock()
			delete(m.processes, "v1")
			existing.status = statusDraining
			existing.restart = false
			m.draining["v1"] = append(m.draining["v1"], existing)
			m.mu.Unlock()
			close(done)
		})
	}
	m.processes["v1"] = existing

	m.reconcileOverride(
		context.Background(),
		oracle.Version{Name: "v1"},
		overrideSrc,
		binPath,
	)

	m.mu.Lock()
	_, running := m.processes["v1"]
	draining := m.draining["v1"]
	m.mu.Unlock()
	if running {
		t.Fatal("override should not restart while its previous child is draining")
	}
	if len(draining) != 1 || draining[0] != existing {
		t.Fatalf("draining children = %v, want the original child", draining)
	}
}

func TestReconcileOverride_DoesNotTrustStaleOverrideFile(t *testing.T) {
	dir := t.TempDir()
	overrideSrc := filepath.Join(dir, "override-source")
	binPath := filepath.Join(dir, "bin", "v1", "testapp")
	downloadedPath := filepath.Join(dir, "bin", "v1", "download-sha", "testapp")
	script := []byte(`#!/bin/sh
case "$1" in
--print-binary-version) echo "testapp-v1" ;;
--print-protocol-version) echo "v1" ;;
*) exec sleep 30 ;;
esac
`)
	if err := os.WriteFile(overrideSrc, script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, script, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BinDir:         filepath.Join(dir, "bin"),
		DataDir:        filepath.Join(dir, "data"),
		BinaryName:     "testapp",
		BasePort:       5000,
		DrainKillGrace: 100 * time.Millisecond,
	})
	done := make(chan struct{})
	var cancelOnce sync.Once
	existing := &child{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: sha256Hex([]byte("download archive")),
		binPath:       downloadedPath,
		done:          done,
		status:        statusRunning,
		restart:       true,
	}
	existing.stop = func() {
		cancelOnce.Do(func() {
			close(done)
		})
	}
	m.processes["v1"] = existing
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.reconcileOverride(ctx, oracle.Version{Name: "v1"}, overrideSrc, binPath)

	srcHash, err := download.HashFile(overrideSrc)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	current := m.processes["v1"]
	m.mu.Unlock()
	if current == existing {
		t.Fatal("stale flat override file should not match a per-SHA child")
	}
	if current == nil {
		t.Fatal("override child should replace the downloaded child")
	}
	if current.binPath != binPath {
		t.Fatalf("override child path = %q, want %q", current.binPath, binPath)
	}
	if current.archiveSHA256 != "override:"+srcHash {
		t.Fatalf("override child identity = %q, want override source hash", current.archiveSHA256)
	}

	cancel()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOverride_SameIdentityDoesNotRestart(t *testing.T) {
	dir := t.TempDir()
	overrideSrc := filepath.Join(dir, "override-source")
	binPath := filepath.Join(dir, "bin", "v1", "testapp")
	content := []byte("override binary")
	if err := os.WriteFile(overrideSrc, content, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, content, 0o755); err != nil {
		t.Fatal(err)
	}
	srcHash, err := download.HashFile(overrideSrc)
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BinDir:     filepath.Join(dir, "bin"),
		DataDir:    filepath.Join(dir, "data"),
		BinaryName: "testapp",
		BasePort:   5000,
	})
	cancelled := false
	existing := &child{
		version:       oracle.Version{Name: "v1"},
		archiveSHA256: "override:" + srcHash,
		binPath:       binPath,
		stop:          func() { cancelled = true },
		done:          make(chan struct{}),
		status:        statusRunning,
		restart:       true,
	}
	m.processes["v1"] = existing

	m.reconcileOverride(context.Background(), oracle.Version{Name: "v1"}, overrideSrc, binPath)

	if cancelled {
		t.Fatal("matching override identity should not restart the child")
	}
	if current := m.processes["v1"]; current != existing {
		t.Fatal("matching override identity should keep the current child")
	}
}

func TestDrainAfterProxyWaitsBeforeRequestingChildDrain(t *testing.T) {
	var drainHits atomic.Int32
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/drain":
			drainHits.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/drain/status":
			json.NewEncoder(w).Encode(map[string]int64{"inflight": 0})
		default:
			http.NotFound(w, r)
		}
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
		stop:    func() { close(done) },
	}
	proxyDrained := make(chan struct{})
	started := make(chan struct{})
	drainedDone := make(chan struct{})
	go func() {
		defer close(drainedDone)
		close(started)
		m.drainAfterProxy(c, proxyDrained)
	}()

	<-started
	if drainHits.Load() != 0 {
		t.Fatal("child drain started before proxy requests drained")
	}
	close(proxyDrained)

	select {
	case <-drainedDone:
	case <-time.After(time.Second):
		t.Fatal("child drain did not finish after proxy requests drained")
	}
	if drainHits.Load() != 1 {
		t.Fatalf("drain hits = %d, want 1", drainHits.Load())
	}
}

func TestStopStartWithdrawsRouteAndWaitsForProxyLease(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRequest) }) }
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(func() {
		release()
		backend.Close()
	})

	m := NewManager(config.Config{
		BasePort:     5000,
		DrainTimeout: time.Second,
	})
	childDone := make(chan struct{})
	stopCalled := make(chan struct{})
	var stopOnce sync.Once
	c := &child{
		version:     oracle.Version{Name: "v1"},
		port:        9001,
		done:        childDone,
		status:      statusRunning,
		restart:     true,
		proxyTarget: proxy.NewTarget(strings.TrimPrefix(backend.URL, "http://")),
		stop: func() {
			stopOnce.Do(func() {
				close(stopCalled)
				close(childDone)
			})
		},
	}
	m.mu.Lock()
	m.processes["v1"] = c
	m.rebuildRoutes()
	m.mu.Unlock()

	proxyServer := httptest.NewServer(proxy.Handler(m.RouteTable()))
	defer proxyServer.Close()
	requestDone := make(chan error, 1)
	go func() {
		resp, err := http.Get(proxyServer.URL + "/v1/work")
		if resp != nil {
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("proxy request did not acquire the old target")
	}

	m.mu.Lock()
	proxyDrained, retired := m.retireChildForStopStartLocked(c)
	m.mu.Unlock()
	if !retired {
		t.Fatal("current child was not retired")
	}
	if _, routed := m.RouteTable().Load().(proxy.RouteTable)["v1"]; routed {
		t.Fatal("retired child remained published in the route table")
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- m.stopRetiredChild(context.Background(), c, proxyDrained)
	}()
	select {
	case <-stopCalled:
		t.Fatal("child stopped while an acquired proxy request was still running")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("child was not stopped after its proxy lease drained")
	}
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
		stop: func() {
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

func TestWaitForChildServingReadyFallsBackToHealthzWhenDefaultReadyMissing(
	t *testing.T,
) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer shutdown()

	if !waitForChildServingReady(
		context.Background(),
		&child{port: port},
		"/ready",
		time.Second,
	) {
		t.Fatal("expected /ready 404 to fall back to /healthz")
	}
}

func TestWaitForChildServingReadyDoesNotFallbackForCustomReadyPath(
	t *testing.T,
) {
	port, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer shutdown()

	if waitForChildServingReady(
		context.Background(),
		&child{port: port},
		"/custom-ready",
		200*time.Millisecond,
	) {
		t.Fatal("custom ready path should not use legacy fallback")
	}
}

func TestWaitForChildServingReadyRequiresPublicHealth(t *testing.T) {
	adminPort, adminShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer adminShutdown()

	var publicReady atomic.Bool
	var publicHits atomic.Int32
	publicPort, publicShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicHits.Add(1)
		if r.URL.Path == "/healthz" && publicReady.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "public listener not ready", http.StatusServiceUnavailable)
	}))
	defer publicShutdown()

	c := &child{port: publicPort}
	setTestAdminPort(c, adminPort)
	if waitForChildServingReady(context.Background(), c, "/ready", 200*time.Millisecond) {
		t.Fatal("admin readiness must not hide an unavailable public listener")
	}
	if publicHits.Load() == 0 {
		t.Fatal("public health endpoint was not probed")
	}

	publicReady.Store(true)
	if !waitForChildServingReady(context.Background(), c, "/ready", time.Second) {
		t.Fatal("child should become ready when admin and public endpoints are healthy")
	}
}

func TestWaitForChildServingReadyRechecksAdminAndPublicTogether(t *testing.T) {
	var adminHits atomic.Int32
	adminPort, adminShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminHits.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "admin no longer ready", http.StatusServiceUnavailable)
	}))
	defer adminShutdown()

	var publicHits atomic.Int32
	publicPort, publicShutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicHits.Add(1) == 1 {
			http.Error(w, "public listener not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer publicShutdown()

	c := &child{port: publicPort}
	setTestAdminPort(c, adminPort)
	if waitForChildServingReady(context.Background(), c, "/ready", 250*time.Millisecond) {
		t.Fatal("child became ready without admin and public health at the same time")
	}
	if adminHits.Load() < 2 {
		t.Fatal("admin readiness was not rechecked after public health failed")
	}
}

func TestDrainAndStopBefore_LegacyDrainStatusUsesShortGrace(t *testing.T) {
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
		stop: func() {
			cancelled = true
			close(done)
		},
	}

	start := time.Now()
	m.drainAndStopBefore(c, time.Now().Add(m.cfg.DrainTimeout))
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
		version: oracle.Version{Name: "v1"},
		port:    publicPort,
	}
	setTestAdminPort(c, adminPort)

	if err := m.requestDrain(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	inflight, err := m.fetchInflight(context.Background(), c)
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

func TestDrainAndStopBefore_WaitsForIdleBeforeCancel(t *testing.T) {
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
		stop: func() {
			cancelled.Store(true)
			close(done)
		},
	}

	m.drainAndStopBefore(c, time.Now().Add(m.cfg.DrainTimeout))
	if !cancelled.Load() {
		t.Fatal("idle child should be cancelled after drain")
	}
	if got := atomic.LoadInt32(&statusRequests); got < 2 {
		t.Fatalf("drain status requests = %d, want at least 2", got)
	}
}

func TestDrainAndStopBefore_TimesOutWithInflightWork(t *testing.T) {
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
		stop: func() {
			cancelled = true
			close(done)
		},
	}

	start := time.Now()
	m.drainAndStopBefore(c, time.Now().Add(m.cfg.DrainTimeout))
	if !cancelled {
		t.Fatal("child should be cancelled after drain timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain timeout path took %s", elapsed)
	}
}

func TestWaitChildrenIdlePollsUntilInflightReachesZero(t *testing.T) {
	var idle atomic.Bool
	var firstStatus sync.Once
	firstStatusSeen := make(chan struct{})
	adminPort, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstStatus.Do(func() {
			close(firstStatusSeen)
		})
		inflight := int64(1)
		if idle.Load() {
			inflight = 0
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"inflight": inflight})
	}))
	defer shutdown()

	m := NewManager(config.Config{
		BasePort:          5000,
		DrainPollInterval: 5 * time.Millisecond,
	})
	c := &child{
		version: oracle.Version{Name: "v1"},
		done:    make(chan struct{}),
	}
	setTestAdminPort(c, adminPort)
	m.mu.Lock()
	m.children[c] = struct{}{}
	m.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		result <- m.WaitChildrenIdle(context.Background())
	}()
	select {
	case <-firstStatusSeen:
	case <-time.After(time.Second):
		t.Fatal("child drain status was not polled")
	}
	select {
	case err := <-result:
		t.Fatalf("WaitChildrenIdle returned while work was in flight: %v", err)
	default:
	}

	idle.Store(true)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitChildrenIdle did not return after the child became idle")
	}
}

func TestWaitChildrenIdleReturnsDeadlineWhileWorkRemainsInflight(t *testing.T) {
	adminPort, shutdown := startLocalHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"inflight": 1})
	}))
	defer shutdown()

	m := NewManager(config.Config{
		BasePort:          5000,
		DrainPollInterval: 5 * time.Millisecond,
	})
	c := &child{
		version: oracle.Version{Name: "v1"},
		done:    make(chan struct{}),
	}
	setTestAdminPort(c, adminPort)
	m.mu.Lock()
	m.children[c] = struct{}{}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := m.WaitChildrenIdle(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitChildrenIdle error = %v, want context deadline exceeded", err)
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
		stop: func() {
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
	routes := m.RouteTable().Load().(proxy.RouteTable)
	if routes["v1"].Address() != "localhost:9001" {
		t.Fatalf("route = %q, want old child route", routes["v1"].Address())
	}
}

func TestDownloadAndSwap_PreflightFailureKeepsOldServing(t *testing.T) {
	tests := []struct {
		name        string
		ha          string
		storageMode string
	}{
		{name: "invalid HA declaration", ha: "enabled", storageMode: "postgres"},
		{name: "unsafe HA storage", ha: "on", storageMode: "hybrid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envHADeployment, tt.ha)
			t.Setenv(envNonHAVersions, "")
			dir := t.TempDir()
			newBinary := []byte(`#!/bin/sh
case "$1" in
--print-binary-version) echo "devshardd-new" ;;
--print-protocol-version) echo "v1" ;;
--print-admin-api-version) echo "1" ;;
--print-storage-mode) echo "` + tt.storageMode + `" ;;
*) exit 99 ;;
esac
`)
			zipData, archiveHash := zipBinary(t, "devshardd", newBinary)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(zipData)
			}))
			defer srv.Close()

			m := NewManager(config.Config{
				BinDir:     filepath.Join(dir, "bin"),
				DataDir:    filepath.Join(dir, "data"),
				BinaryName: "devshardd",
				BasePort:   6200,
			})
			stopped := false
			old := &child{
				version:       oracle.Version{Name: "v1"},
				archiveSHA256: sha256Hex([]byte("old-archive")),
				storageMode:   storageModePostgres,
				port:          9001,
				done:          make(chan struct{}),
				status:        statusRunning,
				restart:       true,
				stop:          func() { stopped = true },
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
				t.Fatal("expected replacement preflight error")
			}
			if stopped {
				t.Fatal("replacement preflight failure stopped the serving child")
			}

			m.mu.Lock()
			current := m.processes["v1"]
			_, downloading := m.downloading["v1"]
			m.mu.Unlock()
			if current != old {
				t.Fatal("replacement preflight failure changed the serving child")
			}
			if downloading {
				t.Fatal("downloading marker was not cleared after preflight failure")
			}
			routes := m.RouteTable().Load().(proxy.RouteTable)
			if routes["v1"].Address() != "localhost:9001" {
				t.Fatalf("route = %q, want old child route", routes["v1"].Address())
			}
		})
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
		stop:          func() { cancelled = true },
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

func TestReconcile_PromotesLegacyCachedVersionWithoutDownload(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	archiveHash := sha256Hex([]byte("legacy archive"))
	binary := []byte(`#!/bin/sh
case "$1" in
--print-binary-version) echo "testapp-v1" ;;
--print-protocol-version) echo "v1" ;;
*) exec sleep 30 ;;
esac
`)
	legacyBinPath := writeLegacyInstall(t, binDir, "v1", archiveHash, "testapp", binary)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	m := NewManager(config.Config{
		BinDir:         binDir,
		DataDir:        filepath.Join(dir, "data"),
		BinaryName:     "testapp",
		BasePort:       5000,
		DrainKillGrace: 100 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Reconcile(ctx, []oracle.Version{{
		Name:   "v1",
		Binary: srv.URL,
		SHA256: archiveHash,
	}}); err != nil {
		t.Fatal(err)
	}

	if got := requests.Load(); got != 0 {
		t.Fatalf("download requests = %d, want 0", got)
	}
	canonicalBinPath := m.installBinPath("v1", archiveHash)
	matches, _, _, err := installedVersionMatches(
		m.installDir("v1", archiveHash),
		canonicalBinPath,
		archiveHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("promoted per-SHA install should match legacy metadata")
	}
	assertPathExists(t, legacyBinPath)
	assertPathExists(t, filepath.Join(filepath.Dir(legacyBinPath), download.InstallMetadataFilename))
	m.mu.Lock()
	current := m.processes["v1"]
	m.mu.Unlock()
	if current == nil {
		t.Fatal("promoted legacy version should start")
	}
	if current.binPath != canonicalBinPath {
		t.Fatalf("started binary = %q, want promoted path %q", current.binPath, canonicalBinPath)
	}

	cancel()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResolveInstalledArtifact_UsesLegacyWhenPromotionFails(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	archiveHash := sha256Hex([]byte("legacy archive"))
	legacyBinPath := writeLegacyInstall(
		t,
		binDir,
		"v1",
		archiveHash,
		"testapp",
		[]byte("legacy binary"),
	)
	m := NewManager(config.Config{BinDir: binDir, BinaryName: "testapp", BasePort: 5000})

	// Keep a non-empty directory at the canonical binary path so atomic rename
	// fails without making the verified legacy source unreadable.
	canonicalBinPath := m.installBinPath("v1", archiveHash)
	if err := os.MkdirAll(filepath.Join(canonicalBinPath, "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}

	artifact, ok := m.resolveInstalledArtifact("v1", archiveHash)
	if !ok {
		t.Fatal("verified legacy install should remain usable when promotion fails")
	}
	if artifact.binPath != legacyBinPath {
		t.Fatalf("resolved binary = %q, want legacy path %q", artifact.binPath, legacyBinPath)
	}
}

func TestResolveInstalledArtifact_ConcurrentPromotionsConverge(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	archiveHash := sha256Hex([]byte("legacy archive"))
	legacyBinPath := writeLegacyInstall(
		t,
		binDir,
		"v1",
		archiveHash,
		"testapp",
		[]byte("legacy binary"),
	)
	cfg := config.Config{BinDir: binDir, BinaryName: "testapp", BasePort: 5000}
	managers := []*Manager{NewManager(cfg), NewManager(cfg)}
	type result struct {
		artifact installedArtifact
		ok       bool
	}
	results := make(chan result, len(managers))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, manager := range managers {
		wg.Add(1)
		go func(m *Manager) {
			defer wg.Done()
			<-start
			artifact, ok := m.resolveInstalledArtifact("v1", archiveHash)
			results <- result{artifact: artifact, ok: ok}
		}(manager)
	}
	close(start)
	wg.Wait()
	close(results)

	canonicalBinPath := managers[0].installBinPath("v1", archiveHash)
	for result := range results {
		if !result.ok {
			t.Fatal("concurrent promotion did not resolve an install")
		}
		if result.artifact.binPath != canonicalBinPath {
			t.Fatalf("resolved binary = %q, want %q", result.artifact.binPath, canonicalBinPath)
		}
	}
	matches, _, _, err := installedVersionMatches(
		managers[0].installDir("v1", archiveHash),
		canonicalBinPath,
		archiveHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("concurrently promoted install should be complete and verified")
	}
	assertPathExists(t, legacyBinPath)
}

func TestResolveInstalledArtifact_RejectsInvalidLegacyInstall(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	archiveHash := sha256Hex([]byte("legacy archive"))
	legacyBinPath := writeLegacyInstall(
		t,
		binDir,
		"v1",
		archiveHash,
		"testapp",
		[]byte("original binary"),
	)
	if err := os.WriteFile(legacyBinPath, []byte("tampered binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(config.Config{BinDir: binDir, BinaryName: "testapp", BasePort: 5000})

	if artifact, ok := m.resolveInstalledArtifact("v1", archiveHash); ok {
		t.Fatalf("invalid legacy install resolved as %+v", artifact)
	}
	assertPathMissing(t, m.installBinPath("v1", archiveHash))
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

func setTestAdminPort(c *child, port int) {
	c.adminPort.Store(int64(port))
}

func writeStorageModeProbeBinary(t *testing.T, dir, name, storageMode string) string {
	t.Helper()
	binPath := filepath.Join(dir, name)
	script := `#!/bin/sh
case "$1" in
--print-storage-mode) echo "` + storageMode + `" ;;
*) echo "unknown flag: $1" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath
}

func writeStorageModeLegacyBinary(t *testing.T, dir, name string) string {
	t.Helper()
	binPath := filepath.Join(dir, name)
	script := `#!/bin/sh
echo "unknown flag: $1" >&2
exit 2
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath
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

func writeLegacyInstall(
	t *testing.T,
	binDir string,
	versionName string,
	archiveHash string,
	binaryName string,
	binary []byte,
) string {
	t.Helper()
	versionDir := filepath.Join(binDir, versionName)
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(versionDir, binaryName)
	if err := os.WriteFile(binPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := download.WriteInstallMetadata(versionDir, download.InstallMetadata{
		ArchiveSHA256: archiveHash,
		BinarySHA256:  sha256Hex(binary),
	}); err != nil {
		t.Fatal(err)
	}
	return binPath
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

// A child is probed for readiness once when it starts, but it can lose serving
// readiness afterwards, for example when its storage becomes unavailable. If
// versiond kept answering 200 for that version the balancer would hold a route
// open to a host that cannot serve.
func TestServesVersion_FollowsTheChildLosingReadiness(t *testing.T) {
	ready := atomic.Bool{}
	ready.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" && !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := serverPort(t, srv)

	m := NewManager(config.Config{BasePort: 5000, ReadyPath: "/ready"})
	c := &child{status: statusRunning, port: port, version: oracle.Version{Name: "v4"}}
	c.serving.Store(true)
	c.servingAt.Store(time.Now().UnixNano())
	m.mu.Lock()
	m.processes["v4"] = c
	m.rebuildRoutes()
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.watchChildReadiness(ctx, c)

	if !m.ServesVersion("v4") {
		t.Fatal("a running, ready child does not serve its version")
	}

	ready.Store(false)
	if !waitFor(2*time.Second, func() bool { return !m.ServesVersion("v4") }) {
		t.Fatal("child reports unready but versiond still offers the version")
	}

	ready.Store(true)
	if !waitFor(2*time.Second, func() bool { return m.ServesVersion("v4") }) {
		t.Fatal("child is ready again but versiond does not offer the version")
	}

	// An unknown version is not served whatever the child says.
	if m.ServesVersion("v9") {
		t.Fatal("a version with no route is served")
	}
}

// A monitor that has stopped reporting must not leave the answer frozen at
// "ready": the last thing it said gets old, and old is not an answer.
func TestServesVersion_StaleReadinessIsNotReadiness(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000, ReadyPath: "/ready"})
	c := &child{status: statusRunning, port: 1, version: oracle.Version{Name: "v4"}}
	c.serving.Store(true)
	c.servingAt.Store(time.Now().Add(-2 * childReadyStale).UnixNano())
	m.mu.Lock()
	m.processes["v4"] = c
	m.rebuildRoutes()
	m.mu.Unlock()

	if m.ServesVersion("v4") {
		t.Fatal("a readiness answer older than childReadyStale still counts as ready")
	}
}

// Readiness belongs to the generation that was asked. A probe answered by a
// child that has since been swapped out must not decide anything about its
// replacement.
func TestServesVersion_ReadinessIsPerGeneration(t *testing.T) {
	m := NewManager(config.Config{BasePort: 5000, ReadyPath: "/ready"})
	old := &child{status: statusRunning, port: 1, version: oracle.Version{Name: "v4"}}
	old.serving.Store(false)
	old.servingAt.Store(time.Now().UnixNano())

	replacement := &child{status: statusRunning, port: 2, version: oracle.Version{Name: "v4"}}
	replacement.serving.Store(true)
	replacement.servingAt.Store(time.Now().UnixNano())

	m.mu.Lock()
	m.processes["v4"] = replacement
	m.rebuildRoutes()
	m.mu.Unlock()

	if !m.ServesVersion("v4") {
		t.Fatal("the unready generation that was replaced still decides the answer")
	}

	// And the reverse: a healthy old generation cannot vouch for an unready one.
	replacement.serving.Store(false)
	if m.ServesVersion("v4") {
		t.Fatal("the current generation is unready but the version is still offered")
	}
	_ = old
}

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split %s: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return port
}

// The contract the crash-restart path relies on: once the monitor's context is
// cancelled and its goroutine has been awaited, it writes nothing more. A
// monitor that survived into the next process attempt would overwrite that
// attempt's answers with probes of the dead process.
func TestWatchChildReadiness_WritesNothingAfterCancelAndAwait(t *testing.T) {
	ready := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" && !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewManager(config.Config{BasePort: 5000, ReadyPath: "/ready"})
	c := &child{status: statusRunning, port: serverPort(t, srv), version: oracle.Version{Name: "v4"}}
	c.serving.Store(true)
	c.servingAt.Store(time.Now().UnixNano())
	m.mu.Lock()
	m.processes["v4"] = c
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.watchChildReadiness(ctx, c)
	}()

	// The monitor is alive and writing: it must observe the unready child.
	if !waitFor(3*time.Second, func() bool { return !c.serving.Load() }) {
		t.Fatal("monitor never observed the unready child")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled monitor did not exit; the restart path would block on it")
	}

	// From here any write is a bug: flip the child to ready and give a leaked
	// prober ample time to have finished a probe it might have had in flight.
	stamp := c.servingAt.Load()
	ready.Store(true)
	time.Sleep(2 * childReadyInterval)
	if c.serving.Load() || c.servingAt.Load() != stamp {
		t.Fatal("monitor wrote after cancel+await; a restarted child would inherit its answers")
	}
}

// The legacy /healthz fallback is worth one line per generation, not one per
// probe: the monitor asks every second.
func TestNoteLegacyFallbackWarnsOncePerGeneration(t *testing.T) {
	c := &child{version: oracle.Version{Name: "v4"}}
	if !c.legacyWarned.CompareAndSwap(false, false) {
		t.Fatal("fresh child already marked as warned")
	}
	c.noteLegacyFallback("/ready")
	if !c.legacyWarned.Load() {
		t.Fatal("first fallback did not latch the warning")
	}
	// A second call must find the latch set; the log side is the CAS.
	c.noteLegacyFallback("/ready")
}

// The probe distinguishes real readiness from the legacy fallback so the caller
// can decide what to log; the fallback only engages when it is allowed.
func TestReadyEndpointReadyReportsTheLegacyFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := serverPort(t, srv)
	client := &http.Client{Timeout: time.Second}

	ready, viaLegacy := readyEndpointReady(context.Background(), client, port, "/ready", true)
	if !ready || !viaLegacy {
		t.Fatalf("fallback path: ready=%v viaLegacy=%v, want true/true", ready, viaLegacy)
	}
	ready, viaLegacy = readyEndpointReady(context.Background(), client, port, "/ready", false)
	if ready || viaLegacy {
		t.Fatalf("fallback disallowed: ready=%v viaLegacy=%v, want false/false", ready, viaLegacy)
	}
}

// prepareChildrenForDrain can retire a generation at any point during its long
// preflight, and transitionGenerationLocked deliberately tolerates the stale
// transitions that follow — so only an explicit check before the fork keeps
// BeginHostDrain's contract that restarts are frozen. Without it the retired
// generation still starts an OS process that no route will ever reach, and the
// evacuation has to out-wait its startup for nothing.
func TestRunChildDoesNotForkARetiredGeneration(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "forked")
	bin := filepath.Join(dir, "child.sh")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"--print-binary-version) echo test-1; exit 0 ;;\n" +
		"--print-protocol-version) echo v4; exit 0 ;;\n" +
		"--print-*) echo test-1; exit 0 ;;\n" +
		"esac\n" +
		"touch " + marker + "\n" +
		"exec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BasePort:   5000,
		DataDir:    t.TempDir(),
		BinaryName: "testapp",
		// Long enough that a forked child provably reaches its marker before
		// the failed readiness wait force-stops it: with a zero timeout the
		// SIGKILL races the shell to the touch, and the assertion below would
		// pass even with the guard removed.
		ReadyTimeout: 3 * time.Second,
	})
	c := &child{
		version:     oracle.Version{Name: "v4"},
		binPath:     bin,
		port:        5001,
		status:      statusRetiring,
		done:        make(chan struct{}),
		ready:       make(chan struct{}),
		forceStopCh: make(chan struct{}),
	}
	m.mu.Lock()
	m.children[c] = struct{}{}
	m.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		m.runChild(context.Background(), c)
	}()

	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("runChild did not return promptly for a retired generation")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a retired generation forked a child process after the drain freeze")
	}
}

// BeginHostDrain freezes restarts long before RequestChildrenDrain retires the
// generations. A child that finishes its preflight inside that gap — the
// announce window, the proxy-lease wait — is still `starting` by phase, so the
// phase half of the guard alone would let it fork after the barrier. The
// hostDraining half must stop it.
func TestRunChildDoesNotForkAfterBeginHostDrain(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "forked")
	bin := filepath.Join(dir, "child.sh")
	// The probes sleep, so the preflight is a wide, reliable window in which the
	// test can raise the drain barrier.
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"--print-binary-version) sleep 1; echo test-1; exit 0 ;;\n" +
		"--print-protocol-version) sleep 1; echo v4; exit 0 ;;\n" +
		"--print-*) echo test-1; exit 0 ;;\n" +
		"esac\n" +
		"touch " + marker + "\n" +
		"exec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(config.Config{
		BasePort:     5000,
		DataDir:      t.TempDir(),
		BinaryName:   "testapp",
		ReadyTimeout: 3 * time.Second,
	})
	c := &child{
		version:     oracle.Version{Name: "v4"},
		binPath:     bin,
		port:        5001,
		status:      statusStarting,
		done:        make(chan struct{}),
		ready:       make(chan struct{}),
		forceStopCh: make(chan struct{}),
	}
	m.mu.Lock()
	m.children[c] = struct{}{}
	m.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		m.runChild(context.Background(), c)
	}()

	// Land inside the sleeping preflight, exactly where an evacuation's
	// announce window would find this child.
	time.Sleep(300 * time.Millisecond)
	m.BeginHostDrain()

	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("runChild did not return after the drain barrier")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a child forked after BeginHostDrain froze restarts")
	}
}

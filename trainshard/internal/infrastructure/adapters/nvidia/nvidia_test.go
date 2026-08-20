package nvidia

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"
)

func TestScanDropsBlankLines(t *testing.T) {
	// arrange
	out := []byte("NVIDIA H100 80GB HBM3\r\n\nNVIDIA H100 80GB HBM3\n   \n")

	// act
	lines, err := scan(out)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %q, want 2 cards", lines)
	}
	if lines[0] != "NVIDIA H100 80GB HBM3" {
		t.Fatalf("line = %q, want the carriage return trimmed", lines[0])
	}
}

func TestScanOfNoGPUs(t *testing.T) {
	// act
	lines, err := scan(nil)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %q", lines)
	}
}

func TestParseComputeApps(t *testing.T) {
	// arrange
	lines := []string{
		"3141, GPU-1c1d0a1e-0000-0000-0000-000000000001",
		"3142, GPU-1c1d0a1e-0000-0000-0000-000000000001",
		"[Not Supported]",
		"notapid, GPU-1c1d0a1e-0000-0000-0000-000000000002",
	}

	// act
	apps := parseComputeApps(lines)

	// assert
	if len(apps) != 2 {
		t.Fatalf("apps = %+v, want the unparsable rows dropped", apps)
	}
	if apps[0].pid != 3141 || apps[1].pid != 3142 {
		t.Fatalf("pids = %d, %d", apps[0].pid, apps[1].pid)
	}
	if apps[0].uuid != "GPU-1c1d0a1e-0000-0000-0000-000000000001" {
		t.Fatalf("uuid = %q, want the leading space trimmed", apps[0].uuid)
	}
}

// Two processes on one card is one card in use, which is what InUse counts
func TestParseComputeAppsKeepsEveryProcess(t *testing.T) {
	// arrange
	lines := []string{"1, GPU-a", "2, GPU-a"}

	// act
	apps := parseComputeApps(lines)

	// assert
	if len(apps) != 2 {
		t.Fatalf("apps = %+v", apps)
	}
	if apps[0].uuid != apps[1].uuid {
		t.Fatal("both processes are on the same card")
	}
}

func TestIsGone(t *testing.T) {
	// assert
	if !isGone(syscall.ESRCH) {
		t.Fatal("a process that already exited must not fail the kill")
	}
	if !isGone(fmt.Errorf("kill 42: %w", syscall.ESRCH)) {
		t.Fatal("want the wrapped error recognised too")
	}
	if isGone(syscall.EPERM) {
		t.Fatal("a permission failure is a real failure")
	}
	if isGone(errors.New("boom")) {
		t.Fatal("an unknown error is a real failure")
	}
}

func TestConfigDefaults(t *testing.T) {
	// act
	cfg := Config{}.withDefaults()

	// assert
	if cfg.SMI != "nvidia-smi" || cfg.Timeout != 15*time.Second {
		t.Fatalf("got %+v", cfg)
	}

	// act
	kept := Config{SMI: "/usr/bin/nvidia-smi", Timeout: time.Second}.withDefaults()

	// assert
	if kept.SMI != "/usr/bin/nvidia-smi" || kept.Timeout != time.Second {
		t.Fatalf("defaults overwrote a set value: %+v", kept)
	}
}

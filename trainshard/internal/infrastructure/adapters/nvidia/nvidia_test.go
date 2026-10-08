package nvidia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"trainshard/internal/domain/shared/vo"
)

func TestAFailedQueryCarriesWhatNvidiaSMISaidAndHowToGetTheCardsBack(t *testing.T) {
	// arrange
	smi := filepath.Join(t.TempDir(), "nvidia-smi")
	script := "#!/bin/sh\necho 'Failed to initialize NVML: Unknown Error'\nexit 255\n"
	if err := os.WriteFile(smi, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gpus := New(Config{SMI: smi}, nil, slog.New(slog.DiscardHandler))

	// act
	_, err := gpus.InUse(context.Background(), vo.NodeRef{})

	// assert
	if err == nil {
		t.Fatal("want the failed query reported")
	}
	for _, want := range []string{"exit status 255", "Failed to initialize NVML: Unknown Error", "needs a restart"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("got %q, want it to carry %q", err, want)
		}
	}
}

func TestAQueryThatTimesOutSaysSoWithoutTheRestartHint(t *testing.T) {
	// arrange
	smi := filepath.Join(t.TempDir(), "nvidia-smi")
	if err := os.WriteFile(smi, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gpus := New(Config{SMI: smi, Timeout: 100 * time.Millisecond}, nil, slog.New(slog.DiscardHandler))

	// act
	_, err := gpus.InUse(context.Background(), vo.NodeRef{})

	// assert
	if err == nil || !strings.Contains(err.Error(), "no answer within") || strings.Contains(err.Error(), "needs a restart") {
		t.Fatalf("got %v, want a timeout named as one", err)
	}
}

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
		"WARNING: infoROM is corrupted at gpu 0000:00:04.0",
		"[N/A], [N/A]",
		"notapid, GPU-1c1d0a1e-0000-0000-0000-000000000002",
		"[Insufficient Permissions], MIG-5e7f0000-0000-0000-0000-000000000003",
	}

	// act
	apps := parseComputeApps(lines)

	// assert
	want := []computeApp{
		{pid: 3141, uuid: "GPU-1c1d0a1e-0000-0000-0000-000000000001"},
		{pid: 3142, uuid: "GPU-1c1d0a1e-0000-0000-0000-000000000001"},
		{pid: unknownPID, uuid: "GPU-1c1d0a1e-0000-0000-0000-000000000002"},
		{pid: unknownPID, uuid: "MIG-5e7f0000-0000-0000-0000-000000000003"},
	}
	if !slices.Equal(apps, want) {
		t.Fatalf("apps = %+v, want %+v: a card with an unreadable pid is busy, a line naming no card is nothing", apps, want)
	}
}

func TestAProcessWithAnUnknownPIDIsNeverTheRunsOwn(t *testing.T) {
	// arrange
	gpus := &GPUs{}

	// act
	own := gpus.belongsTo(unknownPID, "abc")

	// assert
	if own {
		t.Fatal("want an unknown pid kept out of what is killed")
	}
}

func TestParseComputeAppsKeepsEveryProcessOnACard(t *testing.T) {
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

func TestParseCardsReadsNameAndMemory(t *testing.T) {
	// arrange
	lines := []string{"NVIDIA H100 80GB HBM3, 81559", "Tesla T4, 15360"}

	// act
	cards, err := parseCards(lines)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].Name != "NVIDIA H100 80GB HBM3" || cards[0].MemoryMiB != 81559 || cards[1].MemoryMiB != 15360 {
		t.Fatalf("cards = %+v", cards)
	}
}

func TestParseCardsRefusesALineWithoutMemory(t *testing.T) {
	// arrange
	cases := []struct {
		name string
		line string
	}{
		{name: "no memory column", line: "NVIDIA H100 80GB HBM3"},
		{name: "memory that is not a number", line: "NVIDIA H100 80GB HBM3, [N/A]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			_, err := parseCards([]string{tc.line})

			// assert
			if err == nil {
				t.Fatalf("parseCards(%q) = nil error", tc.line)
			}
		})
	}
}

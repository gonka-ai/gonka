package process

import (
	"testing"
	"time"

	"versioned/internal/config"
)

func TestNextReadyWindowDoublesUpToMax(t *testing.T) {
	max := 32 * time.Minute
	got := time.Minute
	want := []time.Duration{
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		32 * time.Minute,
		32 * time.Minute,
		32 * time.Minute,
	}
	for i, expect := range want {
		got = nextReadyWindow(got, max)
		if got != expect {
			t.Fatalf("step %d: nextReadyWindow = %v, want %v", i+1, got, expect)
		}
	}
}

func TestNextReadyWindowCapsWhenAlreadyAboveMax(t *testing.T) {
	if got := nextReadyWindow(60*time.Minute, 32*time.Minute); got != 32*time.Minute {
		t.Fatalf("nextReadyWindow = %v, want %v", got, 32*time.Minute)
	}
}

func TestNextReadyWindowHandlesOverflow(t *testing.T) {
	max := 32 * time.Minute
	if got := nextReadyWindow(time.Duration(1)<<62, max); got != max {
		t.Fatalf("nextReadyWindow on overflow = %v, want %v", got, max)
	}
}

func TestReadyWindowEventuallyReachesCeiling(t *testing.T) {
	max := 32 * time.Minute
	w := 60 * time.Second
	attempts := 0
	for w < max {
		w = nextReadyWindow(w, max)
		attempts++
		if attempts > 10 {
			t.Fatalf("window did not reach ceiling: stuck at %v", w)
		}
	}
	if w != max {
		t.Fatalf("final window = %v, want %v", w, max)
	}
	if attempts != 5 {
		t.Fatalf("attempts to reach ceiling = %d, want 5 (60s->2m->4m->8m->16m->32m)", attempts)
	}
}

func TestManagerDefaultsReadyMaxWait(t *testing.T) {
	m := NewManager(config.Config{BinDir: t.TempDir(), DataDir: t.TempDir()})
	if m.cfg.ReadyMaxWait != 32*time.Minute {
		t.Fatalf("default ReadyMaxWait = %v, want 32m", m.cfg.ReadyMaxWait)
	}
}

func TestManagerReadyMaxWaitNeverBelowReadyTimeout(t *testing.T) {
	m := NewManager(config.Config{
		BinDir:       t.TempDir(),
		DataDir:      t.TempDir(),
		ReadyTimeout: 10 * time.Minute,
		ReadyMaxWait: time.Minute,
	})
	if m.cfg.ReadyMaxWait != 10*time.Minute {
		t.Fatalf("ReadyMaxWait = %v, want it raised to ReadyTimeout (10m)", m.cfg.ReadyMaxWait)
	}
}

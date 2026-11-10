package bridge

import (
	"errors"
	"testing"
	"time"
)

type settledStubBridge struct {
	MainnetBridge
	info  *EscrowInfo
	err   error
	delay time.Duration
}

func (s *settledStubBridge) GetEscrow(string) (*EscrowInfo, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.info, s.err
}

func TestSettledWithin_ReportsChainSettledFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{{"settled", true}, {"open", false}} {
		t.Run(tc.name, func(t *testing.T) {
			br := &settledStubBridge{info: &EscrowInfo{EscrowID: "1", Settled: tc.want}}
			got, err := SettledWithin(br, "1", time.Second)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSettledWithin_FailsOpenOnQueryError(t *testing.T) {
	wantErr := errors.New("chain down")
	got, err := SettledWithin(&settledStubBridge{err: wantErr}, "1", time.Second)
	if got {
		t.Fatal("a failed query must not report settled")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestSettledWithin_FailsOpenOnNilInfo(t *testing.T) {
	got, err := SettledWithin(&settledStubBridge{}, "1", time.Second)
	if got {
		t.Fatal("a nil escrow must not report settled")
	}
	if !errors.Is(err, ErrEscrowNotFound) {
		t.Fatalf("error = %v, want ErrEscrowNotFound", err)
	}
}

func TestSettledWithin_FailsOpenOnNilBridge(t *testing.T) {
	got, err := SettledWithin(nil, "1", time.Second)
	if got || err == nil {
		t.Fatalf("nil bridge: settled=%v err=%v, want false + error", got, err)
	}
}

// A chain node that accepts the connection but never answers must not block the
// caller: GetEscrow takes no context, so the timeout is the only bound.
func TestSettledWithin_ReturnsOnTimeoutWhileQueryHangs(t *testing.T) {
	br := &settledStubBridge{info: &EscrowInfo{Settled: true}, delay: 5 * time.Second}

	start := time.Now()
	got, err := SettledWithin(br, "1", 20*time.Millisecond)
	elapsed := time.Since(start)

	if got {
		t.Fatal("a timed-out query must not report settled")
	}
	if !errors.Is(err, ErrEscrowQueryTimeout) {
		t.Fatalf("error = %v, want ErrEscrowQueryTimeout", err)
	}
	if elapsed > time.Second {
		t.Fatalf("returned after %s, want ~20ms", elapsed)
	}
}

func TestSettledWithin_ExhaustedBudgetSkipsQuery(t *testing.T) {
	br := &settledStubBridge{info: &EscrowInfo{Settled: true}, delay: 5 * time.Second}

	start := time.Now()
	got, err := SettledWithin(br, "1", 0)

	if got {
		t.Fatal("an exhausted budget must not report settled")
	}
	if !errors.Is(err, ErrEscrowQueryTimeout) {
		t.Fatalf("error = %v, want ErrEscrowQueryTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("returned after %s, want immediate", elapsed)
	}
}

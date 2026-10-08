package readiness_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shared"
)

var errBusy = fmt.Errorf("%w: deadline exceeded", shared.ErrUnavailable)

func TestProverAsksTheEngineOnceWhileTheAnswerStands(t *testing.T) {
	// arrange
	probe := newProbeStub()
	prover := readiness.NewProver(probe, newClockStub())
	ctx := context.Background()

	// act
	for i := 0; i < 5; i++ {
		if err := prover.GPUContainer(ctx); err != nil {
			t.Fatalf("a healthy engine must answer: %v", err)
		}
	}

	// assert
	if probe.gpuAsked != 1 {
		t.Fatalf("asked the engine %d times, want once while the answer stands", probe.gpuAsked)
	}
}

func TestProofAfterAnAnswer(t *testing.T) {
	provenAt := newClockStub().now
	proven, err := readiness.Proof{}.After(provenAt, nil)
	if err != nil {
		t.Fatalf("a passed check must prove the runtime: %v", err)
	}

	cases := []struct {
		name      string
		proof     readiness.Proof
		since     time.Duration
		answer    error
		wantErr   error
		wantHolds bool
	}{
		{
			name:      "busy engine keeps a proof that still holds",
			proof:     proven,
			since:     readiness.ProofKeeps - time.Second,
			answer:    errBusy,
			wantHolds: true,
		},
		{
			name:    "busy engine cannot keep a proof past its validity",
			proof:   proven,
			since:   readiness.ProofKeeps + time.Second,
			answer:  errBusy,
			wantErr: shared.ErrUnavailable,
		},
		{
			name:    "busy engine proves nothing on its own",
			proof:   readiness.Proof{},
			answer:  errBusy,
			wantErr: shared.ErrUnavailable,
		},
		{
			name:    "refusal undoes a proof that still holds",
			proof:   proven,
			since:   time.Second,
			answer:  errProbe,
			wantErr: errProbe,
		},
		{
			name:      "fresh pass proves an expired runtime again",
			proof:     proven,
			since:     2 * readiness.ProofKeeps,
			wantHolds: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			now := provenAt.Add(tc.since)

			// act
			next, err := tc.proof.After(now, tc.answer)

			// assert
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
			if next.Holds(now) != tc.wantHolds {
				t.Fatalf("proof holds = %v, want %v", next.Holds(now), tc.wantHolds)
			}
		})
	}
}

func TestReadinessStopsVouchingForARuntimeNobodyCanCheck(t *testing.T) {
	// arrange
	m := newMachine()
	prover := readiness.NewProver(m.probe, m.clock)
	collect := func() readiness.Result {
		return readiness.Collect(context.Background(), prover, m.cards, m.claim, m.keys, nodeA, m.spec)
	}
	if first := collect(); !first.Ready {
		t.Fatalf("the first look must prove the runtime: %s", first.Reason())
	}
	m.probe.gpuContainer = errBusy
	m.clock.now = m.clock.now.Add(readiness.ProofKeeps + time.Second)

	// act
	expired := collect()
	m.clock.now = m.clock.now.Add(readiness.ProofKeeps)
	stillBusy := collect()
	m.probe.gpuContainer = nil
	m.clock.now = m.clock.now.Add(time.Second)
	restored := collect()

	// assert
	for i, result := range []readiness.Result{expired, stillBusy} {
		if result.Ready {
			t.Fatalf("look %d: a proof past its validity must not keep the node in the pool", i+1)
		}
		if !strings.Contains(result.Reason(), string(readiness.CheckDockerGPU)) {
			t.Fatalf("look %d: got reason %q, want it to name %s", i+1, result.Reason(), readiness.CheckDockerGPU)
		}
	}
	if !restored.Ready {
		t.Fatalf("a fresh pass must let the node back in: %s", restored.Reason())
	}
}

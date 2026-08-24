package readiness_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shared"
)

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

func TestProverHoldsAProvenRuntimeThroughABusyEngine(t *testing.T) {
	// arrange
	probe, clock := newProbeStub(), newClockStub()
	prover := readiness.NewProver(probe, clock)
	ctx := context.Background()
	if err := prover.GPUContainer(ctx); err != nil {
		t.Fatalf("the first look must prove the runtime: %v", err)
	}
	probe.gpuContainer = fmt.Errorf("%w: deadline exceeded", shared.ErrUnavailable)
	clock.now = clock.now.Add(readiness.ProofKeeps + time.Second)

	// act
	err := prover.GPUContainer(ctx)

	// assert
	if err != nil {
		t.Fatalf("silence from a busy engine must not undo a proven runtime: %v", err)
	}

	probe.gpuContainer = errProbe
	if err := prover.GPUContainer(ctx); err == nil {
		t.Fatal("a refusal must undo the proof")
	}
}

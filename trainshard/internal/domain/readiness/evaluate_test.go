package readiness_test

import (
	"strings"
	"testing"

	"trainshard/internal/domain/readiness"
)

func allPassed() []readiness.Check {
	checks := make([]readiness.Check, 0, len(readiness.Required()))
	for _, name := range readiness.Required() {
		checks = append(checks, readiness.Passed(name))
	}
	return checks
}

func TestEvaluateNeedsEveryRequiredCheck(t *testing.T) {
	// arrange
	partial := allPassed()[1:]

	// act
	full := readiness.Evaluate(allPassed())
	missing := readiness.Evaluate(partial)

	// assert
	if !full.Ready {
		t.Fatalf("all checks passed but node is not ready: %s", full.Reason())
	}
	if missing.Ready {
		t.Fatal("a check that was never run must keep the node out")
	}
	if !strings.Contains(missing.Reason(), "not checked") {
		t.Fatalf("reason must name the missing check, got %q", missing.Reason())
	}
}

func TestEvaluateReportsWhyANodeIsNotPicked(t *testing.T) {
	// arrange
	checks := allPassed()
	checks[0] = readiness.Failed(readiness.CheckDockerGPU, "no nvidia runtime")

	// act
	result := readiness.Evaluate(checks)

	// assert
	if result.Ready {
		t.Fatal("a failed check must keep the node out")
	}
	if !strings.Contains(result.Reason(), "no nvidia runtime") {
		t.Fatalf("reason must be readable from outside, got %q", result.Reason())
	}
}

func TestEvaluateIgnoresChecksItDoesNotRequire(t *testing.T) {
	// arrange
	checks := append(allPassed(), readiness.Failed("something_else", "irrelevant"))

	// act
	result := readiness.Evaluate(checks)

	// assert
	if !result.Ready {
		t.Fatalf("unknown checks must not affect readiness: %s", result.Reason())
	}
}

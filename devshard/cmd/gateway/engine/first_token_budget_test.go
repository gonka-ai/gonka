package engine

import (
	"testing"
	"time"
)

func budgetPolicy() EscalationPolicy {
	return EscalationPolicy{FirstTokenFloor: time.Second, FirstTokenCeiling: 60 * time.Second}
}

// Test flow:
//  1. Build a policy via `budgetPolicy` and compute its baseline first-token curve for 400 input tokens.
//  2. Compute the budget for a host observed answering in 5 seconds.
//  3. Assert the budget exceeds the curve and equals the expected 7.5 seconds.
func TestFirstTokenBudget_GivesRoomToAHostThatReliablyAnswers(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	curve := policy.firstTokenTimeout(400)

	budget := policy.firstTokenBudget(400, 5*time.Second)

	if budget <= curve {
		t.Fatalf("budget = %v, want more than the curve %v", budget, curve)
	}
	if want := 7500 * time.Millisecond; budget != want {
		t.Errorf("budget = %v, want %v", budget, want)
	}
}

// Test flow:
//  1. Build a policy and compute its curve for 400 tokens.
//  2. Compute the budget for hosts observed answering in 75s and 300s.
//  3. Assert each budget falls back to the curve, unrewarded.
func TestFirstTokenBudget_KeepsTheCurveForAHostThatNeverAnswersInTime(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	curve := policy.firstTokenTimeout(400)

	for _, observed := range []time.Duration{75 * time.Second, 300 * time.Second} {
		if budget := policy.firstTokenBudget(400, observed); budget != curve {
			t.Errorf("observed %v: budget = %v, want the curve %v", observed, budget, curve)
		}
	}
}

// Test flow:
//  1. Build a policy and compute its curve for 400 tokens.
//  2. Compute the budget with a zero observed duration (no history).
//  3. Assert the budget equals the curve.
func TestFirstTokenBudget_FallsBackToTheCurveWithoutAHistory(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	curve := policy.firstTokenTimeout(400)

	if budget := policy.firstTokenBudget(400, 0); budget != curve {
		t.Errorf("budget = %v, want the curve %v", budget, curve)
	}
}

// Test flow:
//  1. Build a policy and compute its curve for 400 tokens.
//  2. Compute the budget for a host observed answering in 200 milliseconds.
//  3. Assert the budget still equals the curve rather than shrinking below it.
func TestFirstTokenBudget_NeverShortensTheDeadline(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	curve := policy.firstTokenTimeout(400)

	if budget := policy.firstTokenBudget(400, 200*time.Millisecond); budget != curve {
		t.Errorf("budget = %v, want the curve %v", budget, curve)
	}
}

// Test flow:
//  1. Build a policy with a 4-second ceiling.
//  2. Compute the budget for a host observed answering in 5 seconds.
//  3. Assert the budget is capped at the 4-second ceiling.
func TestFirstTokenBudget_StaysUnderTheConfiguredCeiling(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	policy.FirstTokenCeiling = 4 * time.Second

	if budget := policy.firstTokenBudget(400, 5*time.Second); budget != 4*time.Second {
		t.Errorf("budget = %v, want the ceiling 4s", budget)
	}
}

// Test flow:
//  1. Build a policy and compute its curve for 400 tokens.
//  2. Compute the budget at exactly `firstTokenObservedLimit*curve` and assert it still counts as reliable (the budget exceeds the curve).
//  3. Compute the budget one nanosecond past that limit and assert it falls back to the curve.
func TestFirstTokenBudget_AtTheEdgeOfWhatCountsAsReliable(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	curve := policy.firstTokenTimeout(400)

	if budget := policy.firstTokenBudget(400, firstTokenObservedLimit*curve); budget <= curve {
		t.Errorf("a host exactly at the limit still counts as reliable: budget = %v", budget)
	}
	if budget := policy.firstTokenBudget(400, firstTokenObservedLimit*curve+time.Nanosecond); budget != curve {
		t.Errorf("one nanosecond past the limit must keep the curve: budget = %v", budget)
	}
}

// Test flow:
//  1. Build a policy with no configured ceiling and compute its curve for 400 tokens.
//  2. Compute the budget at the reliability limit.
//  3. Assert the budget stays within the bound derived from the observed limit and slack, even without a configured ceiling.
func TestFirstTokenBudget_StaysBoundedWithoutACeiling(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	policy.FirstTokenCeiling = 0
	curve := policy.firstTokenTimeout(400)

	budget := policy.firstTokenBudget(400, firstTokenObservedLimit*curve)

	if ceiling := firstTokenObservedLimit * firstTokenObservedSlackNumerator * curve / firstTokenObservedSlackDenominator; budget > ceiling {
		t.Errorf("budget = %v, want no more than %v", budget, ceiling)
	}
}

// Test flow:
//  1. Build a policy and compute its curve for 400 tokens.
//  2. Compute the budget for a negative observed duration.
//  3. Assert the budget falls back to the curve rather than reading the negative value as a fast host.
func TestFirstTokenBudget_IgnoresANegativeObservation(t *testing.T) {
	t.Parallel()
	policy := budgetPolicy()
	curve := policy.firstTokenTimeout(400)

	if budget := policy.firstTokenBudget(400, -time.Second); budget != curve {
		t.Errorf("budget = %v, want the curve %v", budget, curve)
	}
}

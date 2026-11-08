package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"devshard/user"
)

func attemptEndingInError(proof *MissProof) AttemptOutcome {
	return AttemptOutcome{
		Nonce:       7,
		Participant: "host-a",
		StartedAt:   time.Unix(1000, 0),
		ReceiptTime: time.Unix(1001, 0),
		Terminal:    TerminalErrorStream,
		MissProof:   proof,
	}
}

func raceEndingIn(attempt AttemptOutcome) RaceOutcome {
	return RaceOutcome{RequestID: "req-1", EscrowID: "escrow-1", Model: "qwen", Attempts: []AttemptOutcome{attempt}}
}

// Test flow:
//  1. Build a race ending in an attempt whose error stream carries a signed `MissProof`.
//  2. Compute its `TimeoutPlan`.
//  3. Assert the plan has one posted step of kind `SettleByMissClaim`, carrying the retained proof and the `TimeoutKindErrorMiss` event kind.
func TestAnErrorTheHostSignedIsSettledAsAMiss(t *testing.T) {
	proof := &MissProof{ResponsePayload: []byte(`{"events":["data: {\"error\":{}}"]}`), Complete: true}

	steps := raceEndingIn(attemptEndingInError(proof)).TimeoutPlan()

	if len(steps) != 1 {
		t.Fatalf("plan = %d steps, want 1", len(steps))
	}
	if !steps[0].Post {
		t.Fatal("the step must be posted")
	}
	if steps[0].Kind != SettleByMissClaim {
		t.Fatalf("kind = %v, want the miss", steps[0].Kind)
	}
	if steps[0].Proof != proof {
		t.Fatal("the step must carry the proof the attempt retained")
	}
	if steps[0].Event.Kind != TimeoutKindErrorMiss {
		t.Fatalf("event kind = %q, want %q", steps[0].Event.Kind, TimeoutKindErrorMiss)
	}
}

// Test flow:
//  1. Build a race ending in an attempt whose error carries no `MissProof`.
//  2. Compute its `TimeoutPlan`.
//  3. Assert the plan falls back to one posted `SettleByVote` step with the `TimeoutKindExecution` event kind.
func TestAnErrorWithoutProofFallsBackToTheVote(t *testing.T) {
	steps := raceEndingIn(attemptEndingInError(nil)).TimeoutPlan()

	if len(steps) != 1 || !steps[0].Post {
		t.Fatalf("plan = %+v, want one posted step", steps)
	}
	if steps[0].Kind != SettleByVote {
		t.Fatalf("kind = %v, want the ordinary vote", steps[0].Kind)
	}
	if steps[0].Event.Kind != TimeoutKindExecution {
		t.Fatalf("event kind = %q, want %q", steps[0].Event.Kind, TimeoutKindExecution)
	}
}

// Test flow:
//  1. Build an attempt ending in a signed error, with `NonceFinished` also set.
//  2. Compute its `TimeoutPlan`.
//  3. Assert the plan still posts a `SettleByMissClaim` step rather than skipping the finished nonce.
func TestAMissIsClaimedEvenThoughTheHostFinished(t *testing.T) {
	attempt := attemptEndingInError(&MissProof{ResponsePayload: []byte(`{"events":["data: {\"error\":{}}"]}`)})
	attempt.NonceFinished = true

	steps := raceEndingIn(attempt).TimeoutPlan()

	if len(steps) != 1 {
		t.Fatalf("plan = %d steps, want 1", len(steps))
	}
	if !steps[0].Post || steps[0].Kind != SettleByMissClaim {
		t.Fatalf("a finished nonce with proof is still claimed as a miss: %+v", steps[0])
	}
}

// Test flow:
//  1. Build an attempt ending in error with no proof and `NonceFinished` set.
//  2. Compute its `TimeoutPlan`.
//  3. Assert the single resulting step is not posted.
func TestAFinishedNonceWithoutProofIsStillSkipped(t *testing.T) {
	attempt := attemptEndingInError(nil)
	attempt.NonceFinished = true

	steps := raceEndingIn(attempt).TimeoutPlan()

	if len(steps) != 1 {
		t.Fatalf("plan = %d steps, want 1", len(steps))
	}
	if steps[0].Post {
		t.Fatalf("a finished nonce without proof must not be posted: %+v", steps[0])
	}
}

// Test flow:
//  1. Build a `SessionTimeouts` poster over a `scriptedTimeoutHandler` that holds a signed Finish.
//  2. Settle a `SettleByMissClaim` timeout step through it.
//  3. Assert the miss handler ran once and the ordinary vote handler did not run.
func TestAClaimedMissGoesToTheMissHandler(t *testing.T) {
	handler := &scriptedTimeoutHandler{finishTx: []byte("signed-finish")}
	poster := &SessionTimeouts{handler: handler}
	step := TimeoutStep{
		Nonce: 7,
		Kind:  SettleByMissClaim,
		Proof: &MissProof{ResponsePayload: []byte(`{"events":[]}`)},
	}

	if _, err := poster.SettleTimeout(context.Background(), step); err != nil {
		t.Fatalf("SettleTimeout() = %v", err)
	}

	if handler.missCalls != 1 {
		t.Errorf("the miss handler ran %d times, want 1", handler.missCalls)
	}
	if handler.calls != 0 {
		t.Errorf("the vote ran %d times, want the miss instead", handler.calls)
	}
}

// Test flow:
//  1. Build a `SessionTimeouts` poster over a `scriptedTimeoutHandler` holding no Finish.
//  2. Settle a `SettleByMissClaim` timeout step through it.
//  3. Assert the miss handler did not run and the ordinary vote handler ran once instead.
func TestAMissWithNoSignedFinishFallsBackToTheVote(t *testing.T) {
	handler := &scriptedTimeoutHandler{}
	poster := &SessionTimeouts{handler: handler}
	step := TimeoutStep{
		Nonce: 7,
		Kind:  SettleByMissClaim,
		Proof: &MissProof{ResponsePayload: []byte(`{"events":[]}`)},
	}

	if _, err := poster.SettleTimeout(context.Background(), step); err != nil {
		t.Fatalf("SettleTimeout() = %v", err)
	}

	if handler.missCalls != 0 {
		t.Errorf("the miss handler ran %d times, want none without a Finish", handler.missCalls)
	}
	if handler.calls != 1 {
		t.Errorf("the vote ran %d times, want 1", handler.calls)
	}
}

// Test flow:
//  1. Compute `TimeoutOutcome` for a `TimeoutKindErrorMiss` vote whose error wraps `user.ErrInferenceMissed`.
//  2. Assert the action is `TimeoutActionCompleted` with no failure reason.
func TestALandedMissIsReadAsSettledNotFailed(t *testing.T) {
	action, reason := TimeoutOutcome(
		TimeoutVote{Kind: TimeoutKindErrorMiss},
		fmt.Errorf("inference 7 timed out: error: %w", user.ErrInferenceMissed),
		false,
	)

	if action != TimeoutActionCompleted {
		t.Fatalf("action = %q, want %q", action, TimeoutActionCompleted)
	}
	if reason != TimeoutReasonNone {
		t.Fatalf("reason = %q, want %q", reason, TimeoutReasonNone)
	}
}

// Test flow:
//  1. Build a `scriptedTimeoutHandler` whose result reports two verifier rejection reasons for a truncated proof.
//  2. Settle a `SettleByMissClaim` step carrying that truncated proof.
//  3. Assert the returned vote carries both rejection reasons and marks completeness as truncated.
func TestARefusedClaimReportsWhatTheVerifiersSaid(t *testing.T) {
	handler := &scriptedTimeoutHandler{
		finishTx: []byte("signed-finish"),
		result:   user.TimeoutResult{Reason: "error", VerifyRejects: []string{"hash_mismatch", "no_payload"}},
	}
	poster := &SessionTimeouts{handler: handler}
	step := TimeoutStep{
		Nonce: 7,
		Kind:  SettleByMissClaim,
		Proof: &MissProof{ResponsePayload: []byte(`{"events":[]}`), Truncated: true},
	}

	vote, _ := poster.SettleTimeout(context.Background(), step)

	if len(vote.VerifyRejects) != 2 {
		t.Fatalf("verify rejects = %q, want both causes", vote.VerifyRejects)
	}
	if vote.Completeness != MissProofTruncated {
		t.Fatalf("completeness = %q, want %q", vote.Completeness, MissProofTruncated)
	}
}

// Test flow:
//  1. Settle a plain timeout step carrying no proof.
//  2. Assert the returned vote's completeness is left unnamed.
func TestAnOrdinaryVoteNamesNoCompleteness(t *testing.T) {
	poster := &SessionTimeouts{handler: &scriptedTimeoutHandler{}}

	vote, _ := poster.SettleTimeout(context.Background(), TimeoutStep{Nonce: 7})

	if vote.Completeness != MissProofNone {
		t.Fatalf("completeness = %q, want it unnamed", vote.Completeness)
	}
}

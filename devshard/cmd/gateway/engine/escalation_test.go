package engine

import (
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/types"
)

var (
	raceStart  = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	testPolicy = judgedOnly(EscalationPolicyFromConfig(config.Defaults().Engine))
	streaming  = EscalationRequest{InputTokens: 1_000}
)

func judgedOnly(policy EscalationPolicy) EscalationPolicy {
	policy.HedgeFirstTokenFloor = 0
	return policy
}

func dispatched(offset time.Duration) EscalationAttempt {
	return EscalationAttempt{ClockStart: raceStart.Add(offset)}
}

// Test flow:
//  1. Build an `EscalationPolicyFromConfig` from a `config.Engine` with every millisecond-tuned field set.
//  2. Assert the resulting `EscalationPolicy` converts each field to its `time.Duration` equivalent unchanged.
func TestEscalationPolicyFromConfigConvertsEveryTunable(t *testing.T) {
	policy := EscalationPolicyFromConfig(config.Engine{
		ReceiptTimeoutMS:       1_500,
		FirstTokenFloorMS:      250,
		FirstTokenCeilingMS:    70_000,
		InterChunkStallMS:      7_000,
		LoserGraceMS:           90_000,
		MaxAttemptsPerRequest:  4,
		HedgeFirstTokenFloorMS: 1_200,
	})
	want := EscalationPolicy{
		ReceiptTimeout:        1_500 * time.Millisecond,
		FirstTokenFloor:       250 * time.Millisecond,
		FirstTokenCeiling:     70 * time.Second,
		InterChunkStall:       7 * time.Second,
		LoserGrace:            90 * time.Second,
		MaxAttemptsPerRequest: 4,
		HedgeFirstTokenFloor:  1_200 * time.Millisecond,
	}
	if policy != want {
		t.Fatalf("EscalationPolicyFromConfig = %+v, want %+v", policy, want)
	}
}

// Test flow:
//  1. For each table case's budget and primary suspicion/degradation flags, call `Decide`.
//  2. Assert a healthy primary with budget starts alone and waits for the ladder; a suspicious or degraded primary with room for a second attempt starts two at once, naming the matching reason; either case with no budget for a second attempt still starts only one; and a primary that is both reports the suspicious reason it was pinned for.
func TestDecideStartsOneAttemptUnlessThePrimaryIsDistrusted(t *testing.T) {
	testCases := []struct {
		name              string
		budget            int
		primarySuspicious bool
		primaryDegraded   bool
		want              StartPlan
	}{
		{
			name:   "healthy primary starts alone and waits for the ladder",
			budget: 4,
			want:   StartPlan{ImmediateAttempts: 1, Reason: StartPrimary},
		},
		{
			name:              "suspicious primary starts a second attempt at once",
			budget:            2,
			primarySuspicious: true,
			want:              StartPlan{ImmediateAttempts: 2, Reason: StartPrimarySuspicious},
		},
		{
			name:              "suspicious primary with no budget for a second attempt still waits",
			budget:            1,
			primarySuspicious: true,
			want:              StartPlan{ImmediateAttempts: 1, Reason: StartPrimary},
		},
		{
			name:            "degraded primary starts a second attempt at once",
			budget:          2,
			primaryDegraded: true,
			want:            StartPlan{ImmediateAttempts: 2, Reason: StartPrimaryDegraded},
		},
		{
			name:            "degraded primary with no budget for a second attempt still waits",
			budget:          1,
			primaryDegraded: true,
			want:            StartPlan{ImmediateAttempts: 1, Reason: StartPrimary},
		},
		{
			name:              "a primary that is both reports the crown denial it was pinned for",
			budget:            2,
			primarySuspicious: true,
			primaryDegraded:   true,
			want:              StartPlan{ImmediateAttempts: 2, Reason: StartPrimarySuspicious},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := testPolicy.Decide(testCase.budget, testCase.primarySuspicious, testCase.primaryDegraded)
			if got != testCase.want {
				t.Fatalf("Decide = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's `MaxAttemptsPerRequest` knob, host count, and nonce-scarcity flag, call `AttemptBudget`.
//  2. Assert the budget is bounded by the host group when the knob is unset or above it, follows the knob when it is smaller, drops to one for a single or empty host group, and drops to one whenever nonces are scarce.
func TestAttemptBudgetClampsToOneWhenNoncesAreScarce(t *testing.T) {
	testCases := []struct {
		name           string
		maxSpeculative int
		hostCount      int
		nonceScarce    bool
		want           int
	}{
		{name: "unset knob is bounded by the host group", maxSpeculative: 0, hostCount: 5, want: 5},
		{name: "knob below the host count wins", maxSpeculative: 3, hostCount: 5, want: 3},
		{name: "knob equal to the host count", maxSpeculative: 5, hostCount: 5, want: 5},
		{name: "knob above the host count is clamped", maxSpeculative: 9, hostCount: 5, want: 5},
		{name: "single host allows a single attempt", maxSpeculative: 3, hostCount: 1, want: 1},
		{name: "empty host group still yields one", maxSpeculative: 3, hostCount: 0, want: 1},
		{name: "scarce nonces force one attempt", maxSpeculative: 3, hostCount: 5, nonceScarce: true, want: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := EscalationPolicy{MaxAttemptsPerRequest: testCase.maxSpeculative}
			got := policy.AttemptBudget(testCase.hostCount, testCase.nonceScarce)
			if got != testCase.want {
				t.Fatalf("AttemptBudget(%d, %t) = %d, want %d",
					testCase.hostCount, testCase.nonceScarce, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's host count and nonce-scarcity flag, call `AttemptLimit`.
//  2. Assert the limit tracks the host group size, floors at one for an empty group, and drops to one whenever nonces are scarce.
func TestAttemptLimitIsTheHostGroupUnlessNoncesAreScarce(t *testing.T) {
	testCases := []struct {
		name        string
		hostCount   int
		nonceScarce bool
		want        int
	}{
		{name: "every host in the group may be tried", hostCount: 5, want: 5},
		{name: "a single host allows a single attempt", hostCount: 1, want: 1},
		{name: "an empty host group still yields one", hostCount: 0, want: 1},
		{name: "scarce nonces allow no replacement", hostCount: 5, nonceScarce: true, want: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			policy := EscalationPolicy{MaxAttemptsPerRequest: 2}
			if got := policy.AttemptLimit(testCase.hostCount, testCase.nonceScarce); got != testCase.want {
				t.Fatalf("AttemptLimit(%d, %t) = %d, want %d",
					testCase.hostCount, testCase.nonceScarce, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's attempt state and request (an already-escalated attempt, a suspicious attempt, a finished attempt with and without an empty stream, a failed streaming attempt, an undispatched attempt, a dispatched attempt awaiting receipt, a streaming attempt that already produced a token, and a receipted attempt still waiting on its first token — where the first-token floor pushes its deadline out to receipt time plus the floor, since the raw curve would fire before the receipt even landed), call `triggerFor`.
//  2. Assert each rule fires (or does not) with the expected stage and deadline.
func TestLadderRuleInIsolation(t *testing.T) {
	receiptedNoToken := EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second)}
	testCases := []struct {
		name         string
		attempt      EscalationAttempt
		request      EscalationRequest
		wantStage    EscalationStage
		wantDeadline time.Time
	}{
		{
			name:      "rule 0: an attempt that already escalated never triggers again",
			attempt:   EscalationAttempt{Escalated: true, Suspicious: true, ClockStart: raceStart},
			request:   streaming,
			wantStage: StageNone,
		},
		{
			name:         "rule 1: a suspicious attempt escalates immediately",
			attempt:      EscalationAttempt{Suspicious: true, ClockStart: raceStart},
			request:      streaming,
			wantStage:    StageSuspicious,
			wantDeadline: raceStart,
		},
		{
			name:      "rule 3: a finished attempt needs no successor",
			attempt:   EscalationAttempt{Done: true, NonceFinished: true, ClockStart: raceStart},
			request:   streaming,
			wantStage: StageNone,
		},
		{
			name:         "rule 4: a finished attempt that answered with an empty stream escalates immediately",
			attempt:      EscalationAttempt{Done: true, NonceFinished: true, EmptyStream: true, ClockStart: raceStart},
			request:      streaming,
			wantStage:    StageAttemptFailed,
			wantDeadline: raceStart,
		},
		{
			name:         "rule 5: a failed streaming attempt escalates immediately",
			attempt:      EscalationAttempt{Done: true, ClockStart: raceStart},
			request:      streaming,
			wantStage:    StageAttemptFailed,
			wantDeadline: raceStart,
		},
		{
			name:      "rule 6: an undispatched attempt has no deadline to measure from",
			attempt:   EscalationAttempt{},
			request:   streaming,
			wantStage: StageNone,
		},
		{
			name:         "rule 7: a dispatched attempt without a receipt waits the receipt timeout",
			attempt:      dispatched(0),
			request:      streaming,
			wantStage:    StageReceiptTimeout,
			wantDeadline: raceStart.Add(5 * time.Second),
		},
		{
			name:      "rule 9: a streaming attempt that produced a token is on its own",
			attempt:   EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second), FirstToken: raceStart.Add(2 * time.Second)},
			request:   streaming,
			wantStage: StageNone,
		},
		{
			name:         "rule 10: a receipted streaming attempt waits the first-token deadline",
			attempt:      receiptedNoToken,
			request:      streaming,
			wantStage:    StageFirstToken,
			wantDeadline: raceStart.Add(7 * time.Second),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			armed, ok := testPolicy.triggerFor(&testCase.attempt, testCase.request, raceStart)
			if testCase.wantStage == StageNone {
				if ok {
					t.Fatalf("triggerFor = %+v, want no trigger", armed)
				}
				return
			}
			if !ok {
				t.Fatalf("triggerFor: want stage %q, got no trigger", testCase.wantStage)
			}
			if armed.Stage != testCase.wantStage {
				t.Fatalf("stage = %q, want %q", armed.Stage, testCase.wantStage)
			}
			if !armed.Deadline.Equal(testCase.wantDeadline) {
				t.Fatalf("deadline = %v, want %v", armed.Deadline, testCase.wantDeadline)
			}
		})
	}
}

// Test flow:
//  1. For each table case's attempt state (undispatched, finished, awaiting receipt, already escalated, a judged receipt deadline, receipted and awaiting first token, a late receipt after its deadline was judged, a judged first-token deadline, an attempt that already produced a token), call `owedDeadline`.
//  2. Assert the owed stage and deadline match the case, and that a deadline already judged for its stage is never owed again.
func TestOwedDeadlineIsTheEscalationDeadlineJudgedOncePerStage(t *testing.T) {
	policy := EscalationPolicy{ReceiptTimeout: 5 * time.Second, FirstTokenFloor: 6 * time.Second}
	testCases := []struct {
		name         string
		attempt      EscalationAttempt
		wantStage    EscalationStage
		wantDeadline time.Time
	}{
		{
			name:      "an undispatched attempt owes nothing",
			attempt:   EscalationAttempt{},
			wantStage: StageNone,
		},
		{
			name:      "a finished attempt owes nothing",
			attempt:   EscalationAttempt{Done: true, ClockStart: raceStart},
			wantStage: StageNone,
		},
		{
			name:         "an attempt without a receipt owes the receipt deadline",
			attempt:      dispatched(0),
			wantStage:    StageReceiptTimeout,
			wantDeadline: raceStart.Add(5 * time.Second),
		},
		{
			name:         "an attempt that already escalated still owes its deadline",
			attempt:      EscalationAttempt{Escalated: true, ClockStart: raceStart},
			wantStage:    StageReceiptTimeout,
			wantDeadline: raceStart.Add(5 * time.Second),
		},
		{
			name:      "a judged receipt deadline is not owed again",
			attempt:   EscalationAttempt{ClockStart: raceStart, ReceiptDeadlineJudged: true},
			wantStage: StageNone,
		},
		{
			name:         "a receipted attempt owes the first-token deadline",
			attempt:      EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second)},
			wantStage:    StageFirstToken,
			wantDeadline: raceStart.Add(7 * time.Second),
		},
		{
			name:         "a receipt that came after its judged deadline still owes the first-token deadline",
			attempt:      EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second), ReceiptDeadlineJudged: true},
			wantStage:    StageFirstToken,
			wantDeadline: raceStart.Add(7 * time.Second),
		},
		{
			name:      "a judged first-token deadline is not owed again",
			attempt:   EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second), FirstTokenDeadlineJudged: true},
			wantStage: StageNone,
		},
		{
			name:      "an attempt that produced a token owes nothing",
			attempt:   EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second), FirstToken: raceStart.Add(2 * time.Second)},
			wantStage: StageNone,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stage, deadline, owed := policy.owedDeadline(&testCase.attempt, streaming)
			if testCase.wantStage == StageNone {
				if owed {
					t.Fatalf("owedDeadline = (%q, %v), want nothing owed", stage, deadline)
				}
				return
			}
			if !owed || stage != testCase.wantStage || !deadline.Equal(testCase.wantDeadline) {
				t.Fatalf("owedDeadline = (%q, %v, %t), want (%q, %v, true)", stage, deadline, owed, testCase.wantStage, testCase.wantDeadline)
			}
		})
	}
}

// Test flow:
//  1. For each table case's input token count (just under, exactly at, and just over the large-input boundary), call `receiptTimeout`.
//  2. Assert the timeout only doubles once the input strictly exceeds the boundary.
func TestReceiptTimeoutDoublesOnlyAboveTheLargeInputBoundary(t *testing.T) {
	testCases := []struct {
		name        string
		inputTokens uint64
		want        time.Duration
	}{
		{"just under the boundary", 99_999, 5 * time.Second},
		{"exactly at the boundary", 100_000, 5 * time.Second},
		{"just over the boundary", 100_001, 10 * time.Second},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testPolicy.receiptTimeout(testCase.inputTokens); got != testCase.want {
				t.Fatalf("receiptTimeout(%d) = %v, want %v", testCase.inputTokens, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's policy and input token count (a curve just under, on, and just over a configured floor; an unfloored curve at zero and growing input; and a prompt so large its curve — which would otherwise reach nearly nine minutes, past the streaming backstop that cancels the attempt — is capped at the ceiling instead), call `firstTokenTimeout`.
//  2. Assert the returned wait matches the floor, the curve, or the ceiling as the case demands.
func TestFirstTokenTimeoutHoldsTheFloorAndGrowsWithInput(t *testing.T) {
	floored := EscalationPolicy{FirstTokenFloor: 3_988 * time.Millisecond}
	curveOnly := EscalationPolicy{FirstTokenCeiling: testPolicy.FirstTokenCeiling}
	testCases := []struct {
		name        string
		policy      EscalationPolicy
		inputTokens uint64
		want        time.Duration
	}{
		{"curve just under the floor", floored, 43_000, 3_988 * time.Millisecond},
		{"curve exactly on the floor", floored, 44_000, 3_988 * time.Millisecond},
		{"curve just over the floor", floored, 44_001, 3_988_074 * time.Microsecond},
		{"empty prompt sits on the curve", curveOnly, 0, 1_700 * time.Millisecond},
		{"linear term grows the wait", curveOnly, 1_000, 1_730_500 * time.Microsecond},
		{"a prompt no retry could beat stops at the ceiling", testPolicy, 1_000_000, testPolicy.FirstTokenCeiling},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.policy.firstTokenTimeout(testCase.inputTokens); got != testCase.want {
				t.Fatalf("firstTokenTimeout(%d) = %v, want %v", testCase.inputTokens, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a mix of a dispatched attempt, an already-escalated attempt, an earlier-dispatched attempt, and an undispatched attempt.
//  2. Call `NextEscalation`.
//  3. Assert it picks the earliest live deadline among them, naming that attempt's index and deadline.
func TestNextEscalationPicksTheEarliestDeadline(t *testing.T) {
	attempts := []EscalationAttempt{
		dispatched(0),
		{Escalated: true, ClockStart: raceStart.Add(-time.Hour)},
		dispatched(-2 * time.Second),
		{},
	}

	armed, ok := testPolicy.NextEscalation(raceStart, attempts, streaming)

	if !ok {
		t.Fatal("NextEscalation: want a trigger, got none")
	}
	if armed.Attempt != 2 {
		t.Fatalf("Attempt = %d, want 2 (the earliest live deadline)", armed.Attempt)
	}
	if want := raceStart.Add(3 * time.Second); !armed.Deadline.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", armed.Deadline, want)
	}
}

// Test flow:
//  1. Build a finished attempt and an already-escalated attempt.
//  2. Call `NextEscalation`.
//  3. Assert it reports no trigger.
func TestNextEscalationReportsNothingWhenNoAttemptQualifies(t *testing.T) {
	attempts := []EscalationAttempt{
		{Done: true, NonceFinished: true, ClockStart: raceStart},
		{Escalated: true, ClockStart: raceStart},
	}

	if armed, ok := testPolicy.NextEscalation(raceStart, attempts, streaming); ok {
		t.Fatalf("NextEscalation = %+v, want no trigger", armed)
	}
}

// Test flow:
//  1. Arm the next escalation for a freshly dispatched attempt.
//  2. Confirm it against a snapshot where the attempt has since receipted and streamed a token, and assert rejection.
//  3. Confirm it against a snapshot where the attempt has receipted but the stage has since advanced past receipt-timeout, and assert rejection.
//  4. Confirm it against the original, still-silent snapshot and assert it now succeeds at the receipt-timeout stage.
func TestConfirmRejectsAnEscalationWhoseConditionCleared(t *testing.T) {
	attempts := []EscalationAttempt{dispatched(0)}
	armed, ok := testPolicy.NextEscalation(raceStart, attempts, streaming)
	if !ok {
		t.Fatal("NextEscalation on a dispatched attempt: want a trigger, got none")
	}
	fireTime := armed.Deadline

	receipted := []EscalationAttempt{{
		ClockStart:  raceStart,
		ReceiptTime: raceStart.Add(400 * time.Millisecond),
		FirstToken:  raceStart.Add(900 * time.Millisecond),
	}}
	if confirmed, ok := testPolicy.Confirm(armed, fireTime, receipted, streaming); ok {
		t.Fatalf("Confirm on a receipted, streaming attempt = %+v, want rejection", confirmed)
	}

	stillSilent := []EscalationAttempt{{ClockStart: raceStart, ReceiptTime: raceStart.Add(400 * time.Millisecond)}}
	if confirmed, ok := testPolicy.Confirm(armed, fireTime, stillSilent, streaming); ok {
		t.Fatalf("Confirm after the stage advanced to first-token = %+v, want rejection", confirmed)
	}

	confirmed, ok := testPolicy.Confirm(armed, fireTime, attempts, streaming)
	if !ok {
		t.Fatal("Confirm on an attempt that never receipted: want the escalation, got rejection")
	}
	if confirmed != (ConfirmedEscalation{Attempt: 0, Stage: StageReceiptTimeout}) {
		t.Fatalf("Confirm = %+v, want attempt 0 at %q", confirmed, StageReceiptTimeout)
	}
}

// Test flow:
//  1. Arm the next escalation for a dispatched attempt.
//  2. For each table case's fire time (one nanosecond early, exactly on the deadline, one nanosecond late), call `Confirm`.
//  3. Assert confirmation only succeeds at or after the armed deadline.
func TestConfirmHonoursTheDeadlineBoundary(t *testing.T) {
	attempts := []EscalationAttempt{dispatched(0)}
	armed, ok := testPolicy.NextEscalation(raceStart, attempts, streaming)
	if !ok {
		t.Fatal("NextEscalation on a dispatched attempt: want a trigger, got none")
	}
	testCases := []struct {
		name     string
		fireTime time.Time
		want     bool
	}{
		{"one nanosecond early", armed.Deadline.Add(-time.Nanosecond), false},
		{"exactly on the deadline", armed.Deadline, true},
		{"one nanosecond late", armed.Deadline.Add(time.Nanosecond), true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, ok := testPolicy.Confirm(armed, testCase.fireTime, attempts, streaming); ok != testCase.want {
				t.Fatalf("Confirm at %v = %t, want %t", testCase.fireTime, ok, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's out-of-range `ArmedEscalation` index (negative, past the last attempt), call `Confirm`.
//  2. Assert it is rejected.
func TestConfirmRejectsAnAttemptIndexOutsideTheRace(t *testing.T) {
	attempts := []EscalationAttempt{dispatched(0)}
	testCases := []struct {
		name  string
		armed ArmedEscalation
	}{
		{"negative index", ArmedEscalation{Attempt: -1, Stage: StageReceiptTimeout, Deadline: raceStart}},
		{"index past the last attempt", ArmedEscalation{Attempt: 1, Stage: StageReceiptTimeout, Deadline: raceStart}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, ok := testPolicy.Confirm(testCase.armed, raceStart.Add(time.Minute), attempts, streaming); ok {
				t.Fatal("Confirm on an out-of-range attempt: want rejection")
			}
		})
	}
}

// Test flow:
//  1. Arm the next escalation across two dispatched attempts.
//  2. Mark the armed attempt as already escalated.
//  3. Confirm the same armed escalation and assert it is rejected, since its escalation was already spent.
func TestConfirmSpendsOnlyTheArmedAttemptsEscalation(t *testing.T) {
	attempts := []EscalationAttempt{dispatched(0), dispatched(0)}
	armed, ok := testPolicy.NextEscalation(raceStart, attempts, streaming)
	if !ok {
		t.Fatal("NextEscalation with two dispatched attempts: want a trigger, got none")
	}

	attempts[armed.Attempt].Escalated = true

	if confirmed, ok := testPolicy.Confirm(armed, armed.Deadline, attempts, streaming); ok {
		t.Fatalf("Confirm on an already-escalated attempt = %+v, want rejection", confirmed)
	}
}

// Test flow:
//  1. For each `EscalationStage` value, call `Reason()`.
//  2. Assert it returns the stage's expected label string, and an unnamed stage returns an empty string.
func TestStageReasonLabelsEveryTrigger(t *testing.T) {
	testCases := []struct {
		stage EscalationStage
		want  string
	}{
		{StageSuspicious, "suspicious_host"},
		{StageAttemptFailed, "attempt_failed"},
		{StageReceiptTimeout, "receipt_timeout"},
		{StageFirstToken, "first_token_timeout"},
		{StageHedge, "slow_start"},
		{StageNone, ""},
	}
	for _, testCase := range testCases {
		t.Run(string(testCase.stage), func(t *testing.T) {
			if got := testCase.stage.Reason(); got != testCase.want {
				t.Fatalf("%q.Reason() = %q, want %q", testCase.stage, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build an attempt whose receipt landed later than the first-token curve alone would allow.
//  2. Call `triggerFor` at the moment of that receipt.
//  3. Assert it arms the first-token stage with a deadline still ahead of the receipt, not one already past due.
func TestAReceiptAlwaysBuysItsHostFirstTokenGrace(t *testing.T) {
	slowReceipt := EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(4 * time.Second)}

	armed, ok := testPolicy.triggerFor(&slowReceipt, streaming, raceStart.Add(4*time.Second))

	if !ok || armed.Stage != StageFirstToken {
		t.Fatalf("triggerFor = (%q, %v), want the first-token rung", armed.Stage, ok)
	}
	if !armed.Deadline.After(raceStart.Add(4 * time.Second)) {
		t.Fatalf("deadline = %v, want it after the receipt rather than already due", armed.Deadline)
	}
}

// Test flow:
//  1. Compute the chain's default execution timeout.
//  2. Assert the engine's streaming hard timeout stays strictly inside it.
func TestTheStreamingBackstopFiresInsideTheChainsExecutionDeadline(t *testing.T) {
	t.Parallel()
	settlementDeadline := types.DefaultExecutionTimeoutSeconds * time.Second

	if streamingHardTimeout >= settlementDeadline {
		t.Fatalf("streamingHardTimeout = %s, want it inside the chain's %s execution deadline",
			streamingHardTimeout, settlementDeadline)
	}
}

// Test flow:
//  1. Build the policy from the shipped config defaults.
//  2. Assert its attempt budget over a 16-host group is 2.
//  3. Assert its first-token wait for a median prompt sits at the shipped 6-second floor.
func TestShippedDefaultsBoundTheRaceAndItsFirstTokenWait(t *testing.T) {
	t.Parallel()
	policy := EscalationPolicyFromConfig(config.Defaults().Engine)

	if budget := policy.AttemptBudget(16, false); budget != 2 {
		t.Fatalf("AttemptBudget over a 16-host group = %d, want 2", budget)
	}
	if wait := policy.firstTokenTimeout(3_460); wait != 6*time.Second {
		t.Fatalf("first-token wait for a median prompt = %v, want the 6s floor", wait)
	}
}

// Test flow:
//  1. For each table case's policy, attempt state and request size (no receipt with the curve beating the floor, a growing curve with the prompt, a hedge floor above the curve, a zero hedge floor falling back to the receipt deadline, a receipt without a token under both a real and a zero hedge floor, a curve overtaken by the receipt or first-token deadline for a large prompt, and an attempt's fleet scale that moves the hedge out, stays above the floor, or passes the receipt deadline), call `NextEscalation`.
//  2. Assert it arms the expected stage and deadline.
func TestAHedgeArmsOnTheCurveBeforeTheJudgedDeadline(t *testing.T) {
	hedging := EscalationPolicy{
		ReceiptTimeout:       5 * time.Second,
		FirstTokenFloor:      6 * time.Second,
		FirstTokenCeiling:    30 * time.Second,
		HedgeFirstTokenFloor: 1_500 * time.Millisecond,
	}
	emptyPrompt := EscalationRequest{InputTokens: 0}
	largePrompt := EscalationRequest{InputTokens: 100_000}
	receipted := EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second)}
	testCases := []struct {
		name         string
		policy       EscalationPolicy
		attempt      EscalationAttempt
		request      EscalationRequest
		wantStage    EscalationStage
		wantDeadline time.Time
	}{
		{
			name:         "no receipt yet: the curve beats the floor and hedges before the receipt deadline",
			policy:       hedging,
			attempt:      dispatched(0),
			request:      emptyPrompt,
			wantStage:    StageHedge,
			wantDeadline: raceStart.Add(1_700 * time.Millisecond),
		},
		{
			name:         "no receipt yet: the curve grows with the prompt",
			policy:       hedging,
			attempt:      dispatched(0),
			request:      streaming,
			wantStage:    StageHedge,
			wantDeadline: raceStart.Add(1_730_500 * time.Microsecond),
		},
		{
			name:         "a hedge floor above the curve binds",
			policy:       EscalationPolicy{ReceiptTimeout: 5 * time.Second, FirstTokenFloor: 6 * time.Second, HedgeFirstTokenFloor: 2 * time.Second},
			attempt:      dispatched(0),
			request:      emptyPrompt,
			wantStage:    StageHedge,
			wantDeadline: raceStart.Add(2 * time.Second),
		},
		{
			name:         "a zero hedge floor waits the receipt deadline as before",
			policy:       judgedOnly(hedging),
			attempt:      dispatched(0),
			request:      emptyPrompt,
			wantStage:    StageReceiptTimeout,
			wantDeadline: raceStart.Add(5 * time.Second),
		},
		{
			name:         "receipted without a token: the hedge does not wait receipt plus the floor",
			policy:       hedging,
			attempt:      receipted,
			request:      emptyPrompt,
			wantStage:    StageHedge,
			wantDeadline: raceStart.Add(1_700 * time.Millisecond),
		},
		{
			name:         "receipted without a token under a zero hedge floor waits the judged deadline",
			policy:       judgedOnly(hedging),
			attempt:      receipted,
			request:      emptyPrompt,
			wantStage:    StageFirstToken,
			wantDeadline: raceStart.Add(7 * time.Second),
		},
		{
			name:         "a curve past the receipt deadline leaves the receipt deadline in charge",
			policy:       hedging,
			attempt:      dispatched(0),
			request:      largePrompt,
			wantStage:    StageReceiptTimeout,
			wantDeadline: raceStart.Add(5 * time.Second),
		},
		{
			name:         "a curve equal to the first-token deadline leaves the judged stage in charge",
			policy:       hedging,
			attempt:      receipted,
			request:      largePrompt,
			wantStage:    StageFirstToken,
			wantDeadline: raceStart.Add(9_700 * time.Millisecond),
		},
		{
			name:         "a slow fleet moves the hedge out to its own level",
			policy:       hedging,
			attempt:      EscalationAttempt{ClockStart: raceStart, FleetHedgeScale: 2},
			request:      emptyPrompt,
			wantStage:    StageHedge,
			wantDeadline: raceStart.Add(3_400 * time.Millisecond),
		},
		{
			name:         "a fast fleet keeps the hedge floor",
			policy:       hedging,
			attempt:      EscalationAttempt{ClockStart: raceStart, FleetHedgeScale: 0.5},
			request:      emptyPrompt,
			wantStage:    StageHedge,
			wantDeadline: raceStart.Add(1_500 * time.Millisecond),
		},
		{
			name:         "a fleet level past the receipt deadline leaves the receipt deadline in charge",
			policy:       hedging,
			attempt:      EscalationAttempt{ClockStart: raceStart, FleetHedgeScale: 4},
			request:      emptyPrompt,
			wantStage:    StageReceiptTimeout,
			wantDeadline: raceStart.Add(5 * time.Second),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			armed, ok := testCase.policy.NextEscalation(raceStart, []EscalationAttempt{testCase.attempt}, testCase.request)
			if !ok {
				t.Fatalf("NextEscalation: want stage %q, got no trigger", testCase.wantStage)
			}
			if armed.Stage != testCase.wantStage || !armed.Deadline.Equal(testCase.wantDeadline) {
				t.Fatalf("NextEscalation = (%q, %v), want (%q, %v)", armed.Stage, armed.Deadline, testCase.wantStage, testCase.wantDeadline)
			}
		})
	}
}

// Test flow:
//  1. For each table case's attempt state (no receipt, a receipt), call `owedDeadline` under a hedging policy.
//  2. Assert the owed stage and deadline still match the judged ladder, unaffected by hedging.
func TestAHedgeLeavesTheJudgedDeadlineWhereItWas(t *testing.T) {
	hedging := EscalationPolicy{ReceiptTimeout: 5 * time.Second, FirstTokenFloor: 6 * time.Second, HedgeFirstTokenFloor: 1_500 * time.Millisecond}
	testCases := []struct {
		name         string
		attempt      EscalationAttempt
		wantStage    EscalationStage
		wantDeadline time.Time
	}{
		{"no receipt owes the receipt deadline", dispatched(0), StageReceiptTimeout, raceStart.Add(5 * time.Second)},
		{"a receipt owes the floored first-token deadline", EscalationAttempt{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second)}, StageFirstToken, raceStart.Add(7 * time.Second)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stage, deadline, owed := hedging.owedDeadline(&testCase.attempt, streaming)
			if !owed || stage != testCase.wantStage || !deadline.Equal(testCase.wantDeadline) {
				t.Fatalf("owedDeadline = (%q, %v, %t), want (%q, %v, true)", stage, deadline, owed, testCase.wantStage, testCase.wantDeadline)
			}
		})
	}
}

// Test flow:
//  1. Arm the next escalation for a dispatched attempt under a hedging policy and assert it arms the hedge stage.
//  2. Confirm it one nanosecond before the hedge deadline and assert rejection; confirm it exactly on the deadline and assert success.
//  3. Confirm the same armed hedge against a snapshot where the attempt has since receipted before the hedge fired, and assert it still succeeds.
//  4. Confirm it again after marking the attempt as already escalated, and assert rejection.
func TestConfirmHonoursTheHedgeDeadline(t *testing.T) {
	hedging := EscalationPolicy{ReceiptTimeout: 5 * time.Second, FirstTokenFloor: 6 * time.Second, HedgeFirstTokenFloor: 1_500 * time.Millisecond}
	attempts := []EscalationAttempt{dispatched(0)}
	armed, ok := hedging.NextEscalation(raceStart, attempts, streaming)
	if !ok || armed.Stage != StageHedge {
		t.Fatalf("NextEscalation = (%+v, %t), want a hedge", armed, ok)
	}

	if confirmed, ok := hedging.Confirm(armed, armed.Deadline.Add(-time.Nanosecond), attempts, streaming); ok {
		t.Fatalf("Confirm before the hedge deadline = %+v, want rejection", confirmed)
	}
	confirmed, ok := hedging.Confirm(armed, armed.Deadline, attempts, streaming)
	if !ok || confirmed != (ConfirmedEscalation{Attempt: 0, Stage: StageHedge}) {
		t.Fatalf("Confirm on the hedge deadline = (%+v, %t), want attempt 0 at %q", confirmed, ok, StageHedge)
	}

	receipted := []EscalationAttempt{{ClockStart: raceStart, ReceiptTime: raceStart.Add(time.Second)}}
	if _, ok := hedging.Confirm(armed, raceStart.Add(3*time.Second), receipted, streaming); !ok {
		t.Fatal("Confirm of a hedge whose receipt landed before it fired: want the escalation, got rejection")
	}

	attempts[0].Escalated = true
	if confirmed, ok := hedging.Confirm(armed, raceStart.Add(3*time.Second), attempts, streaming); ok {
		t.Fatalf("Confirm after the hedge already escalated = %+v, want rejection", confirmed)
	}
}

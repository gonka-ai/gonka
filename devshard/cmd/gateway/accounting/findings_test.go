package accounting

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"devshard/cmd/gateway/engine"
)

// recordWith builds the totals a finding reads from an assigned count and a disposition breakdown.
func recordWith(assigned uint64, dispositions map[Disposition]uint64) ParticipantRecord {
	return ParticipantRecord{nonceTotals: nonceTotals{Assigned: assigned, Dispositions: dispositions}}
}

// troubledBook drives a book through the facts a struggling host produces, on a group of one so every
// nonce lands on the same participant.
func troubledBook(t *testing.T) *Book {
	t.Helper()
	const total, unfinished = 100, 10
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, total); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	attempts := make([]Attempt, 0, total)
	for nonce := uint64(1); nonce <= total; nonce++ {
		attempts = append(attempts, Attempt{
			Nonce: nonce, Sent: true, Acknowledged: true, Usage: UsageWinner,
		})
	}
	if err := book.RecordRace(testEscrow, attempts); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	finishedNonces := make([]uint64, 0, total-unfinished)
	for nonce := uint64(unfinished + 1); nonce <= total; nonce++ {
		finishedNonces = append(finishedNonces, nonce)
	}
	if err := book.MarkFinished(testEscrow, finishedNonces); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}
	for nonce := uint64(1); nonce <= unfinished; nonce++ {
		if err := book.RecordTimeout(testEscrow, nonce, "execution", "completed", "none"); err != nil {
			t.Fatalf("RecordTimeout(%d): %v", nonce, err)
		}
	}
	return book
}

// Test flow:
//  1. Build a `troubledBook` fixture and query its records.
//  2. Assert the query returns the single participant of a group of one.
//  3. Assert that record's findings carry `FindingExecutionTimeouts`.
func TestQueryAttachesFindingsToTheRecord(t *testing.T) {
	records := troubledBook(t).Query(QueryFilter{})

	if len(records) != 1 {
		t.Fatalf("got %d records, want the single participant of a group of one", len(records))
	}
	findingWithCode(t, records[0].Findings, FindingExecutionTimeouts)
}

func codesOf(findings []Finding) []string {
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	return codes
}

func findingWithCode(t *testing.T, findings []Finding, code string) Finding {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return finding
		}
	}
	t.Fatalf("findings %v carry no %s", codesOf(findings), code)
	return Finding{}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 90 finished and used, 10 unfinished by execution.
//  2. Find the `FindingExecutionTimeouts` finding for it.
//  3. Assert its severity is critical at the 10% rate.
//  4. Assert its part and whole are 10 of 100.
func TestAcknowledgedButUnfinishedNoncesAreReported(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:        90,
		DispositionUnfinishedExecution: 10,
	})

	finding := findingWithCode(t, findingsFor(record), FindingExecutionTimeouts)

	if finding.Severity != SeverityCritical {
		t.Fatalf("severity = %s, want critical at 10%%", finding.Severity)
	}
	if finding.Part != 10 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want 10 of 100", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 5 assigned nonces, 1 finished and used, 4 unfinished by execution.
//  2. Assert findings are empty, below the volume floor.
func TestARateOverTooFewNoncesIsNotReported(t *testing.T) {
	record := recordWith(5, map[Disposition]uint64{
		DispositionFinishedUsed:        1,
		DispositionUnfinishedExecution: 4,
	})

	if findings := findingsFor(record); len(findings) != 0 {
		t.Fatalf("findings = %v, want none below the volume floor", codesOf(findings))
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, all finished and used.
//  2. Assert findings are empty.
func TestACleanParticipantIsReportedAsClean(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 100})

	if findings := findingsFor(record); len(findings) != 0 {
		t.Fatalf("findings = %v, want none for a participant that finished everything", codesOf(findings))
	}
}

// Test flow:
//  1. Build a record of 500 assigned nonces: 95 finished and used, 5 unfinished by execution, 400 ghost burns.
//  2. Find the `FindingExecutionTimeouts` finding.
//  3. Assert its part and whole are 5 of 100, excluding the 400 burned nonces from the denominator.
func TestBurnedNoncesDoNotCountAgainstTheHostsFailureRates(t *testing.T) {
	record := recordWith(500, map[Disposition]uint64{
		DispositionFinishedUsed:        95,
		DispositionUnfinishedExecution: 5,
		DispositionGhost:               400,
	})

	finding := findingWithCode(t, findingsFor(record), FindingExecutionTimeouts)

	if finding.Part != 5 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want 5 of 100", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces with 40 ghosted under two reasons: a full window and an open cut-off.
//  2. Find the `FindingGatewayThrottled` finding.
//  3. Assert its part and whole are 40 of 100, both reasons counted as one gateway-side finding.
func TestGatewaySideThrottlingIsReportedAsTheGatewaysOwn(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 60, DispositionGhost: 40})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionGhost, GhostReason: "participant_window_full_no_send"},
		Count:      25,
	}, {
		CounterKey: CounterKey{Disposition: DispositionGhost, GhostReason: "participant_cut_off_no_send"},
		Count:      15,
	}}

	finding := findingWithCode(t, findingsFor(record), FindingGatewayThrottled)

	if finding.Part != 40 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want the burns measured against assigned nonces", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record with 40 nonces ghosted under `ghostReasonThrottledBeforeTheSplit`, the reason string written before that split existed.
//  2. Find the `FindingGatewayThrottled` finding.
//  3. Assert its part is 40, so the pre-split reason still counts.
func TestTheReasonWrittenBeforeTheBusyBrokenSplitStillCounts(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 60, DispositionGhost: 40})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionGhost, GhostReason: ghostReasonThrottledBeforeTheSplit},
		Count:      40,
	}}

	finding := findingWithCode(t, findingsFor(record), FindingGatewayThrottled)

	if finding.Part != 40 {
		t.Fatalf("finding = %d of %d, want the rows written before the split counted too", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 10 assigned nonces, all finished and used, with 3 nonces overcounted.
//  2. Find the `FindingLedgerOvercounted` finding.
//  3. Assert its severity is warning, since no host behaviour produces this.
func TestLedgerOvercountingIsAlwaysReported(t *testing.T) {
	record := recordWith(10, map[Disposition]uint64{DispositionFinishedUsed: 10})
	record.Overcounted = 3

	finding := findingWithCode(t, findingsFor(record), FindingLedgerOvercounted)

	if finding.Severity != SeverityWarning {
		t.Fatalf("severity = %s, want warning: no host behaviour produces this", finding.Severity)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, all finished and used, with 2 nonces overcounted and a cross-check error count of 7.
//  2. Find the `FindingChainDisagreement` finding.
//  3. Assert its part and whole are 5 of 100, the drift left over once the overcount is subtracted.
func TestChainDisagreementReportsTheDriftBesideTheOvercount(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 100})
	record.Overcounted = 2
	record.CrossChecks.ErrorCount = 7

	finding := findingWithCode(t, findingsFor(record), FindingChainDisagreement)

	if finding.Part != 5 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want the 5 drifting nonces the overcount does not explain",
			finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 70 finished and used, 30 finished and unused.
//  2. Find the `FindingUnusedAnswers` finding.
//  3. Assert its part and whole are 30 of 100.
func TestUnusedAnswersReadAgainstWhatWasDelivered(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:   70,
		DispositionFinishedUnused: 30,
	})

	finding := findingWithCode(t, findingsFor(record), FindingUnusedAnswers)

	if finding.Part != 30 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want 30 of 100", finding.Part, finding.Whole)
	}
}

func recordCrossing(assigned uint64, delivered uint64, key CounterKey, crossings uint64) ParticipantRecord {
	record := recordWith(assigned, map[Disposition]uint64{DispositionFinishedUsed: delivered})
	record.Counters = []CounterRecord{
		{CounterKey: key, Count: crossings},
		{CounterKey: CounterKey{Disposition: DispositionFinishedUsed}, Count: delivered - crossings},
	}
	return record
}

// Test flow:
//  1. Build a `recordCrossing` fixture of 200 assigned, 100 delivered, with 20 slow-chunk crossings.
//  2. Find the `FindingSlowChunks` finding.
//  3. Assert its part and whole are 20 of 100.
func TestSlowChunksAreReportedAgainstDeliveredAnswers(t *testing.T) {
	record := recordCrossing(200, 100,
		CounterKey{Disposition: DispositionFinishedUsed, SlowChunk: true}, 20)

	finding := findingWithCode(t, findingsFor(record), FindingSlowChunks)

	if finding.Part != 20 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want 20 of 100", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a `recordCrossing` fixture of 200 assigned, 100 delivered, with 10 clock-drift crossings.
//  2. Find the `FindingClockDrift` finding.
//  3. Assert its part is 10.
func TestDriftedClocksAreReported(t *testing.T) {
	record := recordCrossing(200, 100,
		CounterKey{Disposition: DispositionFinishedUsed, ClockDrifted: true}, 10)

	finding := findingWithCode(t, findingsFor(record), FindingClockDrift)

	if finding.Part != 10 {
		t.Fatalf("finding = %d drifted, want 10", finding.Part)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 80 finished and used, 20 unfinished by execution, split into two "empty_stream" counters: 5 in PoC phase and 15 outside it.
//  2. Find the `FindingFailureTerminals` finding.
//  3. Assert its part is 20, both the PoC and the normal failure counted.
//  4. Sum the counters outside PoC that failed without an answer and assert that sum is 15.
func TestFailureTerminalsCountEveryFailureThatReachedTheHost(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:        80,
		DispositionUnfinishedExecution: 20,
	})
	record.Counters = []CounterRecord{
		{CounterKey: CounterKey{Disposition: DispositionUnfinishedExecution, Terminal: "empty_stream", Phase: PhasePoC}, Count: 5},
		{CounterKey: CounterKey{Disposition: DispositionUnfinishedExecution, Terminal: "empty_stream"}, Count: 15},
	}

	finding := findingWithCode(t, findingsFor(record), FindingFailureTerminals)

	if finding.Part != 20 {
		t.Fatalf("finding = %d failures, want both the PoC and the normal one", finding.Part)
	}
	var outsidePoCFailures uint64
	for _, counter := range record.Counters {
		if outsidePoC(counter.CounterKey) && failedWithoutAnswer(counter.CounterKey) {
			outsidePoCFailures += counter.Count
		}
	}
	if outsidePoCFailures != 15 {
		t.Fatalf("counters outside PoC = %d, want 15: the split lives there now", outsidePoCFailures)
	}
}

// Test flow:
//  1. Build a record of 200 assigned nonces, 100 finished and used, split into slow-chunk counters: 40 in PoC phase, 10 outside it, and 50 with no slow chunk.
//  2. Find the `FindingSlowChunks` finding.
//  3. Assert its part and whole are 10 of 60, excluding the PoC counter from both.
func TestStallsDuringPoCAreNotChargedToTheHost(t *testing.T) {
	record := recordWith(200, map[Disposition]uint64{DispositionFinishedUsed: 100})
	record.Counters = []CounterRecord{
		{CounterKey: CounterKey{Disposition: DispositionFinishedUsed, SlowChunk: true, Phase: PhasePoC}, Count: 40},
		{CounterKey: CounterKey{Disposition: DispositionFinishedUsed, SlowChunk: true}, Count: 10},
		{CounterKey: CounterKey{Disposition: DispositionFinishedUsed}, Count: 50},
	}

	finding := findingWithCode(t, findingsFor(record), FindingSlowChunks)

	if finding.Part != 10 || finding.Whole != 60 {
		t.Fatalf("finding = %d of %d, want 10 of 60", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 200 assigned nonces, 100 finished and used, 10 unfinished by execution, all tagged `engine.TimeoutReasonLongResponse`.
//  2. Assert findings carry no `FindingExecutionTimeouts` code.
func TestALongResponseTheGatewayExcusedIsNotAFailure(t *testing.T) {
	record := recordWith(200, map[Disposition]uint64{
		DispositionFinishedUsed:        100,
		DispositionUnfinishedExecution: 10,
	})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{
			Disposition:   DispositionUnfinishedExecution,
			TimeoutReason: engine.TimeoutReasonLongResponse,
		},
		Count: 10,
	}}

	if codes := codesOf(findingsFor(record)); slices.Contains(codes, FindingExecutionTimeouts) {
		t.Fatalf("findings = %v, want no execution-timeout finding for nonces the gateway excused", codes)
	}
}

// Test flow:
//  1. Build a `recordCrossing` fixture of 200 assigned, 100 delivered, with 20 slow-decode crossings.
//  2. Find the `FindingSlowDecode` finding.
//  3. Assert its part and whole are 20 of 100.
func TestSlowDecodersAreReportedAgainstDeliveredAnswers(t *testing.T) {
	record := recordCrossing(200, 100,
		CounterKey{Disposition: DispositionFinishedUsed, SlowDecode: true}, 20)

	finding := findingWithCode(t, findingsFor(record), FindingSlowDecode)

	if finding.Part != 20 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want 20 of 100", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a `recordCrossing` fixture of 200 assigned, 100 delivered, with 40 slow-decode crossings tagged PoC phase.
//  2. Assert none of the findings carry the `FindingSlowDecode` code.
func TestASlowDecodeDuringPoCIsNotChargedToTheHost(t *testing.T) {
	record := recordCrossing(200, 100,
		CounterKey{Disposition: DispositionFinishedUsed, SlowDecode: true, Phase: PhasePoC}, 40)

	for _, finding := range findingsFor(record) {
		if finding.Code == FindingSlowDecode {
			t.Fatalf("finding = %+v, want none: a PoC answer is not a serving answer", finding)
		}
	}
}

// Test flow:
//  1. List every finding code an external tracker alerts on, deliberately excluding "blocked_by_capability" since no burn can produce it any more.
//  2. Assert each listed code is still present in `findingCodes`.
func TestTheFindingVocabularyKeepsTheNamesOperatorsAlertOn(t *testing.T) {
	for _, code := range []string{
		"execution_timeouts", "refusals", "answers_unused", "throttled_by_gateway",
		"chain_recorded_misses", "chain_recorded_invalid",
		"challenges_unresolved", "timeouts_undecided", "reasons_unknown",
		"ledger_disagrees_with_chain", "ledger_overcounted", "logprobs_not_token_ids",
		"slow_receipts", "slow_chunks", "clock_drift", "slow_decode",
	} {
		if !slices.Contains(findingCodes, code) {
			t.Errorf("%q is no longer a code this gateway can emit", code)
		}
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, all finished and used, with timeout outcomes: 500 skipped, 15 applied, 3 insufficient votes, 2 vote-collection failures.
//  2. Find the `FindingUndecidedTimeouts` finding.
//  3. Assert its part and whole are 5 of 20, the rounds that voted rather than the skipped ones.
func TestUndecidedTimeoutsAreMeasuredAgainstTheRoundsThatVoted(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 100})
	record.TimeoutOutcomes = map[TimeoutOutcome]uint64{
		TimeoutSkipped:              500,
		TimeoutApplied:              15,
		TimeoutInsufficientVotes:    3,
		TimeoutVoteCollectionFailed: 2,
	}

	finding := findingWithCode(t, findingsFor(record), FindingUndecidedTimeouts)

	if finding.Part != 5 || finding.Whole != 20 {
		t.Fatalf("finding = %d of %d, want 5 undecided of the 20 rounds that voted", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, all finished and used, with 30 ghosted under "participant_state_diverged_no_send".
//  2. Find the `FindingStateDiverged` finding and assert its part is 30.
//  3. Assert the findings carry no `FindingGatewayThrottled` code, so the divergence is not double-counted as throttling.
func TestStateDivergenceBurnsAreReportedApartFromThrottling(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 100})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionGhost, GhostReason: "participant_state_diverged_no_send"},
		Count:      30,
	}}

	findings := findingsFor(record)

	diverged := findingWithCode(t, findings, FindingStateDiverged)
	if diverged.Part != 30 {
		t.Fatalf("diverged = %d, want the 30 burned nonces", diverged.Part)
	}
	if slices.Contains(codesOf(findings), FindingGatewayThrottled) {
		t.Error("a divergence burn was also counted as gateway throttling: one cause, two accusations")
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, all finished and used, with 1 counter flagged `LogprobsDecoded`.
//  2. Find the `FindingDecodedLogprobs` finding.
//  3. Assert its part and whole are 1 of 100, since a single unreplayable answer is already worth reporting.
func TestDecodedLogprobsAreReportedAtOnce(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{DispositionFinishedUsed: 100})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionFinishedUsed, LogprobsDecoded: true},
		Count:      1,
	}}

	finding := findingWithCode(t, findingsFor(record), FindingDecodedLogprobs)

	if finding.Part != 1 || finding.Whole != 100 {
		t.Fatalf("finding = %d of %d, want the one unreplayable answer against what was delivered",
			finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Read `docs/accounting.md`.
//  2. Walk every code in `findingCodes`.
//  3. Assert each code appears backtick-quoted somewhere in the doc.
func TestEveryFindingCodeIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "accounting.md"))
	if err != nil {
		t.Fatalf("reading accounting.md: %v", err)
	}
	for _, code := range findingCodes {
		if !bytes.Contains(doc, []byte("`"+code+"`")) {
			t.Errorf("finding %q is not explained in accounting.md", code)
		}
	}
}

// Test flow:
//  1. For each gateway-caused timeout reason (phase aborted, collection error, not applied, no poster, long response), build a record of 100 assigned nonces, 50 finished and used, 50 unfinished-refused tagged with that reason.
//  2. Assert the findings for each case carry no `FindingRefusals` code.
func TestFailuresThisGatewayCausedAreNotChargedToTheHost(t *testing.T) {
	for _, reason := range []string{
		engine.TimeoutReasonPhaseAborted, engine.TimeoutReasonCollectionError,
		engine.TimeoutReasonNotApplied, engine.TimeoutReasonNoPoster, engine.TimeoutReasonLongResponse,
	} {
		t.Run(reason, func(t *testing.T) {
			record := recordWith(100, map[Disposition]uint64{
				DispositionFinishedUsed:      50,
				DispositionUnfinishedRefused: 50,
			})
			record.Counters = []CounterRecord{{
				CounterKey: CounterKey{Disposition: DispositionUnfinishedRefused, TimeoutReason: reason},
				Count:      50,
			}}

			if codes := codesOf(findingsFor(record)); slices.Contains(codes, FindingRefusals) {
				t.Errorf("findings %v blame the host for a refusal caused by %q", codes, reason)
			}
		})
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 50 finished and used, 50 unfinished-refused with no counter breakdown.
//  2. Find the `FindingRefusals` finding.
//  3. Assert its part is 50, all of it charged.
func TestARefusalWithNoNamedCauseStillCountsAgainstTheHost(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:      50,
		DispositionUnfinishedRefused: 50,
	})

	finding := findingWithCode(t, findingsFor(record), FindingRefusals)

	if finding.Part != 50 {
		t.Fatalf("refusals = %d of %d, want all 50 charged", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. Build a record of 40 assigned nonces, 20 finished and used, 20 finished and unused, all 20 tagged terminal `TerminalWarmupProbe`.
//  2. Assert findings carry no `FindingUnusedAnswers` code.
func TestTheWarmupProbeIsNotAnAnswerNobodyUsed(t *testing.T) {
	record := recordWith(40, map[Disposition]uint64{
		DispositionFinishedUsed:   20,
		DispositionFinishedUnused: 20,
	})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionFinishedUnused, Terminal: TerminalWarmupProbe},
		Count:      20,
	}}

	if codes := codesOf(findingsFor(record)); slices.Contains(codes, FindingUnusedAnswers) {
		t.Errorf("findings %v count the gateway's own probes as answers a client threw away", codes)
	}
}

// Test flow:
//  1. Build a record of 40 assigned nonces, 20 finished and used, 20 finished and unused, with no probe tag.
//  2. Find the `FindingUnusedAnswers` finding.
//  3. Assert its part and whole are 20 of 40.
func TestALostRaceIsStillAnAnswerNobodyUsed(t *testing.T) {
	record := recordWith(40, map[Disposition]uint64{
		DispositionFinishedUsed:   20,
		DispositionFinishedUnused: 20,
	})

	finding := findingWithCode(t, findingsFor(record), FindingUnusedAnswers)

	if finding.Part != 20 || finding.Whole != 40 {
		t.Fatalf("unused = %d of %d, want the 20 lost races against all 40 delivered", finding.Part, finding.Whole)
	}
}

// Test flow:
//  1. For each table case (an abandoned refusal, an abandoned execution), build a record of 100 assigned nonces, 60 finished and used, 40 in the case's disposition, all tagged terminal `TerminalClientCancelled`.
//  2. Assert the findings for that case carry none of the case's finding code.
func TestAClientThatStoppedWaitingIsNotChargedToTheHost(t *testing.T) {
	tests := []struct {
		name        string
		disposition Disposition
		code        string
	}{
		{"an abandoned refusal", DispositionUnfinishedRefused, FindingRefusals},
		{"an abandoned execution", DispositionUnfinishedExecution, FindingExecutionTimeouts},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			record := recordWith(100, map[Disposition]uint64{
				DispositionFinishedUsed: 60,
				testCase.disposition:    40,
			})
			record.Counters = []CounterRecord{{
				CounterKey: CounterKey{Disposition: testCase.disposition, Terminal: TerminalClientCancelled},
				Count:      40,
			}}

			findings := findingsFor(record)

			if slices.Contains(codesOf(findings), testCase.code) {
				t.Fatalf("%q was raised for attempts the client abandoned: %+v", testCase.code, findings)
			}
		})
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 60 finished and used, 40 unfinished-refused tagged terminal "no_receipt" rather than client-cancelled.
//  2. Assert findings carry `FindingRefusals`, since only the client-cancelled shape is excused.
func TestAnAbandonedAttemptIsTheOnlyOneExcused(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:      60,
		DispositionUnfinishedRefused: 40,
	})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionUnfinishedRefused, Terminal: "no_receipt"},
		Count:      40,
	}}

	findings := findingsFor(record)

	if !slices.Contains(codesOf(findings), FindingRefusals) {
		t.Fatalf("refusals was not raised for a host that refused 40 of 100: %+v", findings)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 20 finished and used, 5 unfinished-refused tagged terminal `TerminalWarmupProbe`.
//  2. Assert findings carry no `FindingRefusals` code, whether the probe succeeded or was itself refused.
func TestARefusedWarmupProbeIsNotChargedToTheHost(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:      20,
		DispositionUnfinishedRefused: 5,
	})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionUnfinishedRefused, Terminal: TerminalWarmupProbe},
		Count:      5,
	}}

	if codes := codesOf(findingsFor(record)); slices.Contains(codes, FindingRefusals) {
		t.Errorf("findings %v charge the host for refusing the gateway's own probe", codes)
	}
}

// Test flow:
//  1. Build a record of 100 assigned nonces, 10 finished and used, 10 unfinished by execution, with a warmup probe counter recorded as unfinished-refused rather than delivered.
//  2. Find the `FindingExecutionTimeouts` finding.
//  3. Assert its whole is 20, so the probe's own bucket is subtracted rather than the delivered total.
func TestAProbeThatFailedDoesNotEmptyTheDeliveredTotal(t *testing.T) {
	record := recordWith(100, map[Disposition]uint64{
		DispositionFinishedUsed:        10,
		DispositionUnfinishedExecution: 10,
	})
	record.Counters = []CounterRecord{{
		CounterKey: CounterKey{Disposition: DispositionUnfinishedRefused, Terminal: TerminalWarmupProbe},
		Count:      10,
	}}

	finding := findingWithCode(t, findingsFor(record), FindingExecutionTimeouts)

	if finding.Whole != 20 {
		t.Fatalf("execution timeouts = %d of %d, want 10 of 20 -- the delivered answers still counted", finding.Part, finding.Whole)
	}
}

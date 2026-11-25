package nonces

import (
	"errors"
	"maps"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

func markFinished(t *testing.T, ledger *Recorder, escrowID string, nonces ...uint64) {
	t.Helper()
	if err := ledger.service.Book.MarkFinished(escrowID, nonces); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}
}

func recorderFor(service *accounting.Service) *Recorder {
	return newRecorder(service, nil)
}

func newLedgerForTest(t *testing.T) *Recorder {
	t.Helper()
	service, err := accounting.NewService(accounting.Settings{
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	ledger := recorderFor(service)
	t.Cleanup(func() { _ = ledger.service.Close() })
	if err := ledger.service.Book.OpenEscrow(accounting.EscrowMetadata{
		EscrowID: "escrow-1",
		Model:    "model-a",
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "participant-0"},
			{SlotID: 1, ValidatorAddress: "participant-1"},
		},
	}); err != nil {
		t.Fatalf("OpenEscrow(): %v", err)
	}
	return ledger
}

func dispositionCount(t *testing.T, ledger *Recorder, want accounting.Disposition) uint64 {
	t.Helper()
	var total uint64
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		total += record.Dispositions[want]
	}
	return total
}

// Test flow:
//  1. Record a won race with a winning attempt and a losing attempt on different nonces, then mark both nonces finished as the sweep does.
//  2. Assert `finished_used` counts only the winner.
//  3. Assert `finished_unused` counts the losing attempt, since its work was real but never delivered.
func TestALosingAttemptOfAWonRaceIsCountedAsUnused(t *testing.T) {
	ledger := newLedgerForTest(t)
	sent := time.Unix(100, 0)

	ledger.RecordRace(engine.RaceOutcome{
		EscrowID:    "escrow-1",
		Succeeded:   true,
		WinnerNonce: 4,
		Attempts: []engine.AttemptOutcome{
			{Nonce: 4, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalWon},
			{Nonce: 5, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalLost},
		},
	})
	markFinished(t, ledger, "escrow-1", 4, 5)

	if used := dispositionCount(t, ledger, accounting.DispositionFinishedUsed); used != 1 {
		t.Fatalf("finished_used = %d, want the winner alone", used)
	}
	if unused := dispositionCount(t, ledger, accounting.DispositionFinishedUnused); unused != 1 {
		t.Fatalf("finished_unused = %d, want the losing attempt", unused)
	}
}

// Test flow:
//  1. Record a lost race whose single attempt finished, then mark its nonce finished as the sweep does.
//  2. Assert `finished_usage_unknown` counts that attempt, since a lost race's partial delivery to the client cannot be known.
func TestAFinishedAttemptOfALostRaceIsCountedAsUnknown(t *testing.T) {
	ledger := newLedgerForTest(t)

	ledger.RecordRace(engine.RaceOutcome{
		EscrowID:  "escrow-1",
		Succeeded: false,
		Attempts: []engine.AttemptOutcome{
			{Nonce: 4, SendTime: time.Unix(100, 0), NonceFinished: true, Terminal: engine.TerminalLost},
		},
	})
	markFinished(t, ledger, "escrow-1", 4)

	if unknown := dispositionCount(t, ledger, accounting.DispositionFinishedUsageUnknown); unknown != 1 {
		t.Fatalf("finished_usage_unknown = %d, want the finished attempt", unknown)
	}
}

// Test flow:
//  1. Record a lost race whose attempt never reports a terminal, leaving its nonce pending.
//  2. Assert `unfinished_execution` counts nothing before the timeout settles.
//  3. Record a completed execution timeout for that nonce and escrow.
//  4. Assert `unfinished_execution` now counts the nonce the timeout classified.
func TestATimeoutClassifiesTheNonceItsRaceLeftPending(t *testing.T) {
	ledger := newLedgerForTest(t)

	ledger.RecordRace(engine.RaceOutcome{
		EscrowID:  "escrow-1",
		Succeeded: false,
		Attempts:  []engine.AttemptOutcome{{Nonce: 4, SendTime: time.Unix(100, 0)}},
	})
	if pending := dispositionCount(t, ledger, accounting.DispositionUnfinishedExecution); pending != 0 {
		t.Fatalf("unfinished_execution = %d, want none before the timeout settles", pending)
	}

	ledger.RecordTimeout(engine.TimeoutEvent{
		EscrowID: "escrow-1", Nonce: 4, Kind: "execution",
		Action: engine.TimeoutActionCompleted, Reason: "none",
	})

	if settled := dispositionCount(t, ledger, accounting.DispositionUnfinishedExecution); settled != 1 {
		t.Fatalf("unfinished_execution = %d, want the nonce classified by its timeout", settled)
	}
}

// Test flow:
//  1. Record a ghosted nonce naming a burn reason.
//  2. Query every participant's disposition counts.
//  3. Assert only the participant holding the slot the chain assigns to that nonce (with a two-slot escrow, nonce 5 belongs to slot 1) is charged a ghost, and every other participant is charged none.
func TestABurnedNonceIsCountedAgainstTheSlotTheChainAssignsIt(t *testing.T) {
	ledger := newLedgerForTest(t)

	ledger.RecordGhost("escrow-1", 5, "participant_window_full_no_send")

	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		want := uint64(0)
		if record.Participant == "participant-1" {
			want = 1
		}
		if got := record.Dispositions[accounting.DispositionGhost]; got != want {
			t.Fatalf("%s ghosts = %d, want %d", record.Participant, got, want)
		}
	}
}

// Test flow:
//  1. Call every recording method (ghost, race, timeout, diff facts, probe) and Start/Close on a nil `*Recorder`.
//  2. Assert none of them panic and RecordProbe/Close return no error.
func TestADisabledLedgerAcceptsEveryFactWithoutPanicking(t *testing.T) {
	var ledger *Recorder

	ledger.RecordGhost("escrow-1", 1, "poc_unavailable_host")
	ledger.RecordRace(engine.RaceOutcome{EscrowID: "escrow-1"})
	ledger.RecordTimeout(engine.TimeoutEvent{EscrowID: "escrow-1"})
	ledger.RecordDiffFacts("escrow-1", []accounting.DiffFact{{Kind: accounting.DiffFactValidation}})
	if err := ledger.RecordProbe("escrow-1", accounting.Attempt{Nonce: 1}); err != nil {
		t.Fatalf("RecordProbe() = %v, want nothing refused while disabled", err)
	}
	ledger.Start(t.Context(), nil, nil)
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

// Test flow:
//  1. Open a ledger configured as enabled on a given port.
//  2. Assert it is built with a listener bound to that port.
//  3. Open a second ledger with accounting left disabled.
//  4. Assert `Open` returns nothing for it.
func TestAnEnabledLedgerServesItsAPIOnItsPortAndADisabledOneIsNotBuilt(t *testing.T) {
	ledger := Open(
		config.NonceAccounting{Enabled: true, Port: 9191, SnapshotSeconds: 300},
		t.TempDir(), nil, func() time.Time { return time.Unix(0, 0).UTC() },
	)
	if ledger == nil {
		t.Fatal("Open() returned nothing for an enabled ledger")
	}
	t.Cleanup(func() { _ = ledger.Close() })
	if ledger.listener == nil {
		t.Fatal("Open() built an enabled ledger with no API listener")
	}
	if ledger.listener.Addr != ":9191" {
		t.Fatalf("listener address = %q, want :9191", ledger.listener.Addr)
	}

	disabled := Open(
		config.NonceAccounting{SnapshotSeconds: 300},
		t.TempDir(), nil, func() time.Time { return time.Unix(0, 0).UTC() },
	)
	if disabled != nil {
		t.Fatal("Open() built a disabled ledger")
	}
}

func counterCount(t *testing.T, ledger *Recorder, match func(accounting.CounterKey) bool) uint64 {
	t.Helper()
	var total uint64
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		for _, counter := range record.Counters {
			if match(counter.CounterKey) {
				total += counter.Count
			}
		}
	}
	return total
}

// Test flow:
//  1. Define a table of winning attempts, varying the gap between first content and the last chunk and the reported completion tokens across a slow decode, a fast decode, and a host that reported no tokens at all.
//  2. For each case, record the race outcome and mark the winner nonce finished as the sweep does.
//  3. Assert the `slow_decode` counter matches the case's expectation.
func TestASlowDecodeReachesTheLedgerAsAFactAboutTheHost(t *testing.T) {
	sent := time.Unix(100, 0)
	firstContent := sent.Add(time.Second)

	cases := []struct {
		name      string
		lastChunk time.Time
		tokens    int64
		want      uint64
	}{
		{
			name:      "a hundred tokens over ten seconds is a hundred milliseconds each",
			lastChunk: firstContent.Add(10 * time.Second), tokens: 100, want: 1,
		},
		{
			name:      "the same tokens over one second is ten milliseconds each",
			lastChunk: firstContent.Add(time.Second), tokens: 100,
		},
		{name: "a host that reported no tokens is not judged", lastChunk: firstContent.Add(time.Minute)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := newLedgerForTest(t)

			ledger.RecordRace(engine.RaceOutcome{
				EscrowID: "escrow-1", Succeeded: true, WinnerNonce: 4,
				Attempts: []engine.AttemptOutcome{{
					Nonce: 4, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalWon,
					FirstContent: firstContent, LastChunk: testCase.lastChunk,
					UsageCompletionTokens: testCase.tokens,
				}},
			})
			markFinished(t, ledger, "escrow-1", 4)

			slow := counterCount(t, ledger, func(key accounting.CounterKey) bool { return key.SlowDecode })
			if slow != testCase.want {
				t.Fatalf("counters marked slow_decode = %d, want %d", slow, testCase.want)
			}
		})
	}
}

func terminalsOf(t *testing.T, ledger *Recorder) map[string]uint64 {
	t.Helper()
	terminals := map[string]uint64{}
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		for _, counter := range record.Counters {
			terminals[counter.Terminal] += counter.Count
		}
	}
	return terminals
}

// Test flow:
//  1. Define a table of race lifecycles, varying whether the client was still waiting or had already left when the winner finished.
//  2. For each case, record a won race with a winning and a losing attempt, then mark both nonces finished.
//  3. Assert the winner's terminal is named accordingly (`TerminalWon` or `accounting.TerminalClientGone`).
//  4. Assert the loser's terminal is left untouched.
func TestAWinnerWhoseClientLeftIsNamedApartFromOneThatWasRead(t *testing.T) {
	sent := time.Unix(100, 0)
	tests := []struct {
		name       string
		clientGone bool
		want       string
	}{
		{"the client was still waiting", false, engine.TerminalWon.String()},
		{"the client had already left", true, accounting.TerminalClientGone},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ledger := newLedgerForTest(t)

			ledger.RecordRace(engine.RaceOutcome{
				EscrowID:    "escrow-1",
				Succeeded:   true,
				WinnerNonce: 4,
				Lifecycle:   engine.Lifecycle{ClientGone: testCase.clientGone},
				Attempts: []engine.AttemptOutcome{
					{Nonce: 4, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalWon},
					{Nonce: 5, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalLost},
				},
			})
			markFinished(t, ledger, "escrow-1", 4, 5)

			terminals := terminalsOf(t, ledger)
			if terminals[testCase.want] != 1 {
				t.Fatalf("terminals = %v, want one %q for the winner", terminals, testCase.want)
			}
			if terminals[engine.TerminalLost.String()] != 1 {
				t.Errorf("terminals = %v, want the loser untouched", terminals)
			}
		})
	}
}

// Test flow:
//  1. Record two ghosted burns that name no nonce.
//  2. Assert no participant is charged a ghost disposition, so a burn with no nonce is never misfiled against nonce zero.
func TestABurnWithNoNonceIsNotRecordedAgainstNonceZero(t *testing.T) {
	ledger := newLedgerForTest(t)

	ledger.RecordGhost("escrow-1", 0, "participant_window_full_no_send")
	ledger.RecordGhost("escrow-1", 0, "participant_window_full_no_send")

	for _, record := range ledger.Book().Query(accounting.QueryFilter{}) {
		if ghosts := record.Dispositions[accounting.DispositionGhost]; ghosts != 0 {
			t.Errorf("%s was charged %d ghosts for burns that named no nonce", record.Participant, ghosts)
		}
	}
}

// Test flow:
//  1. Record a validation, an invalid verdict, and an applied timeout as diff facts, two of them naming a validator slot.
//  2. Assert only the participant holding that slot has its validation, invalid-verdict, and timeout counts incremented, and every other participant has none.
func TestDiffFactsLandOnTheSlotsTheChainAssigns(t *testing.T) {
	ledger := newLedgerForTest(t)

	ledger.RecordDiffFacts("escrow-1", []accounting.DiffFact{
		{Kind: accounting.DiffFactValidation, Nonce: 5, ValidatorSlot: 1},
		{Kind: accounting.DiffFactInvalidVerdict, Nonce: 5, ValidatorSlot: 1},
		{Kind: accounting.DiffFactAppliedTimeout, Nonce: 3},
	})

	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		want := uint64(0)
		if record.Participant == "participant-1" {
			want = 1
		}
		if record.ValidationsPerformed != want || record.CrossChecks.RecordedInvalid != want || record.TimeoutsApplied != want {
			t.Fatalf("%s validations/invalid/timeouts = %d/%d/%d, want %d each", record.Participant,
				record.ValidationsPerformed, record.CrossChecks.RecordedInvalid, record.TimeoutsApplied, want)
		}
	}
}

func participantRecord(t *testing.T, ledger *Recorder, participant string) accounting.ParticipantRecord {
	t.Helper()
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		if record.Participant == participant {
			return record
		}
	}
	t.Fatalf("no record for %s", participant)
	return accounting.ParticipantRecord{}
}

// Test flow:
//  1. Observe an escrow state frozen at a fee of 10 per nonce and finalizing from nonce 3, as a sweep reads it.
//  2. Record a heartbeat service fact on nonce 1 and a plain round on nonce 5.
//  3. Assert participant-1 pays 10 for the heartbeat and files the round after the chain's finalize nonce under finalize.
func TestASweepTellsTheLedgerWhatTheChainCharges(t *testing.T) {
	ledger := newLedgerForTest(t)
	ledger.observeEscrowState("escrow-1", types.EscrowState{
		LatestNonce: 5, FinalizeNonce: 3, Config: types.SessionConfig{FeePerNonce: 10},
	})

	ledger.RecordDiffFacts("escrow-1", []accounting.DiffFact{
		{Kind: accounting.DiffFactServiceNonce, Nonce: 1, Purpose: accounting.ServiceHeartbeat},
		{Kind: accounting.DiffFactServiceNonce, Nonce: 5, Purpose: accounting.ServiceFlush},
	})

	record := participantRecord(t, ledger, "participant-1")
	want := map[accounting.ServicePurpose]uint64{accounting.ServiceHeartbeat: 1, accounting.ServiceFinalize: 1}
	if !maps.Equal(record.ServiceNonces, want) || record.ServiceFee != 10 {
		t.Fatalf("service_nonces/service_fee = %v/%d, want %v/10", record.ServiceNonces, record.ServiceFee, want)
	}
}

// Test flow:
//  1. Record a warmup probe attempt.
//  2. Assert it settles with no error.
//  3. Mark its nonce finished, as the sweep does once the chain applied the Finish.
//  4. Assert its terminal is counted under `accounting.TerminalWarmupProbe`, kept apart from every serving ratio.
func TestAProbeLandsUnderItsOwnTerminal(t *testing.T) {
	ledger := newLedgerForTest(t)

	err := ledger.RecordProbe("escrow-1", accounting.Attempt{
		Nonce: 4, Sent: true, Acknowledged: true,
		Usage: accounting.UsageLoser, Phase: accounting.PhaseNormal, Terminal: accounting.TerminalWarmupProbe,
	})
	if err != nil {
		t.Fatalf("RecordProbe() = %v, want the probe settled", err)
	}
	markFinished(t, ledger, "escrow-1", 4)
	if terminals := terminalsOf(t, ledger); terminals[accounting.TerminalWarmupProbe] != 1 {
		t.Fatalf("terminals = %v, want the probe under %q", terminals, accounting.TerminalWarmupProbe)
	}
}

// Test flow:
//  1. Record a warmup probe attempt against an escrow that was never opened.
//  2. Assert the call returns `accounting.ErrUnknownEscrow`.
func TestAProbeOnAnUnopenedEscrowIsRefused(t *testing.T) {
	ledger := newLedgerForTest(t)

	err := ledger.RecordProbe("escrow-9", accounting.Attempt{Nonce: 7, Sent: true, Acknowledged: true, Terminal: accounting.TerminalWarmupProbe})

	if !errors.Is(err, accounting.ErrUnknownEscrow) {
		t.Fatalf("RecordProbe() = %v, want the unknown-escrow refusal", err)
	}
}

// Test flow:
//  1. Record a won race whose request carries a tokenizer count, with a winning and a losing attempt.
//  2. Observe both nonces finished with a byte length far from that count.
//  3. Assert both nonces' estimated input tokens are the race's tokenizer count, marked tokenized.
func TestEveryAttemptOfARaceCarriesTheRequestsTokenizerCountIntoTheLedger(t *testing.T) {
	ledger := newLedgerForTest(t)
	sent := time.Unix(100, 0)

	ledger.RecordRace(engine.RaceOutcome{
		EscrowID:          "escrow-1",
		Succeeded:         true,
		WinnerNonce:       4,
		InputTokens:       21,
		InputTokensSource: filters.InputTokensTokenizerExact,
		Attempts: []engine.AttemptOutcome{
			{Nonce: 4, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalWon},
			{Nonce: 5, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalLost},
		},
	})
	if err := ledger.service.Book.ObserveInferences("escrow-1", map[uint64]*types.InferenceRecord{
		4: {Status: types.StatusFinished, InputLength: 4_000},
		5: {Status: types.StatusFinished, InputLength: 4_000},
	}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	var estimated, tokenized uint64
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		estimated += record.EstimatedInput
		tokenized += record.TokenizedInput
	}
	if estimated != 2*21 || tokenized != 2*21 {
		t.Fatalf("estimated %d, tokenized %d, want both nonces at the race's 21 tokenizer tokens", estimated, tokenized)
	}
}

// Test flow:
//  1. Assign a nonce to a request that carries a tokenizer count, then sweep the escrow with the nonce finished and again with it sealed, before its race reports.
//  2. Report the race, with the sealed nonce as its loser.
//  3. Assert the sealed nonce was estimated by the count it was assigned with, marked tokenized, and no input count is left behind.
func TestALoserSealedBeforeItsRaceReportsKeepsTheCountItWasAssignedWith(t *testing.T) {
	ledger := newLedgerForTest(t)
	sent := time.Unix(100, 0)

	ledger.NonceAssigned("escrow-1", 5, scheduler.RequestProfile{RequestID: "request-1", InputTokens: 21, InputTokensSource: filters.InputTokensTokenizerExact})
	for _, inferences := range []map[uint64]*types.InferenceRecord{{5: {Status: types.StatusFinished, InputLength: 4_000}}, {}} {
		ledger.observeEscrowState("escrow-1", types.EscrowState{LatestNonce: 5, Inferences: inferences})
	}
	ledger.RecordRace(engine.RaceOutcome{
		EscrowID:          "escrow-1",
		RequestID:         "request-1",
		Succeeded:         true,
		WinnerNonce:       6,
		InputTokens:       21,
		InputTokensSource: filters.InputTokensTokenizerExact,
		Attempts: []engine.AttemptOutcome{
			{Nonce: 5, SendTime: sent, NonceFinished: true, Terminal: engine.TerminalLost},
			{Nonce: 6, SendTime: sent, Terminal: engine.TerminalWon},
		},
	})

	var estimated, tokenized uint64
	for _, record := range ledger.service.Book.Query(accounting.QueryFilter{}) {
		estimated += record.EstimatedInput
		tokenized += record.TokenizedInput
	}
	if estimated != 21 || tokenized != 21 {
		t.Fatalf("estimated %d, tokenized %d, want the sealed loser at its 21 tokenizer tokens", estimated, tokenized)
	}
	for _, escrow := range ledger.service.Book.Snapshot().Escrows {
		if counts := escrow.InputCounts; len(counts) != 1 || counts[0].Nonce != 6 {
			t.Fatalf("input counts = %+v, want only the unsealed winner's", counts)
		}
	}
}

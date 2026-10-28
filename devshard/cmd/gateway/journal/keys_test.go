package journal

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

// methodsThatWriteNoLine are the exported methods that write no line of their own.
var methodsThatWriteNoLine = []string{"Close", "Counts", "Flush"}

// declaredKeys reads internal/logkey's string constants, so the vocabulary keeps one definition.
func declaredKeys(t *testing.T) map[string]bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "internal", "logkey", "logkey.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse logkey.go: %v", err)
	}
	keys := map[string]bool{}
	for _, declaration := range parsed.Decls {
		general, isGeneral := declaration.(*ast.GenDecl)
		if !isGeneral || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Values) == 0 {
				continue
			}
			if literal, isLiteral := value.Values[0].(*ast.BasicLit); isLiteral && literal.Kind == token.STRING {
				unquoted, _ := strconv.Unquote(literal.Value)
				keys[unquoted] = true
			}
		}
	}
	return keys
}

// everyProducer drives each producer method with its widest input, so every key a line can carry is written once.
func everyProducer() map[string]func(events *Journal) {
	failure := errors.New("sample failure")
	sent := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	widestAttempt := engine.AttemptOutcome{
		Participant: hostAlpha, Nonce: 7, NonceFinished: true, UsageCompletionTokens: 40,
		SendTime: sent, ReceiptTime: sent.Add(200 * time.Millisecond), FirstToken: sent.Add(400 * time.Millisecond),
		FirstContent: sent.Add(500 * time.Millisecond), Completed: sent.Add(3 * time.Second),
		ContentChunks: 12, StreamChunks: 14, OutputBytes: 900,
		MaxChunkGap: time.Second, MaxChunkGapAt: 3, MeanChunkGap: 200 * time.Millisecond,
		UpstreamStatus: 502, UpstreamBody: "bad gateway", StateDivergent: true, LocalFault: true,
		LastChunkHead:  `data: {"choices":[],"usage":{"completion_tokens":0}}`,
		FirstChunkHead: `data: {"choices":[{"delta":{}}],"prompt_token_ids":[...]}`,
		FinishReason:   "length", UsagePromptTokens: 49_019, LogprobTokens: 5,
		Confirmed: true, ConfirmedAt: sent.Add(100 * time.Millisecond).Unix(),
	}
	widestOutcome := engine.RaceOutcome{
		RequestID: "request-1", EscrowID: "escrow-1", Model: "qwen", InputTokens: 12,
		WinnerNonce: 7, Succeeded: true, Attempts: []engine.AttemptOutcome{widestAttempt},
	}
	failedVote := engine.TimeoutEvent{
		RequestID: "request-1", EscrowID: "escrow-1", Participant: hostAlpha, Model: "qwen", Nonce: 7,
		Kind: engine.TimeoutKindExecution, Action: engine.TimeoutActionFailed, Reason: engine.TimeoutReasonNotApplied,
	}
	return map[string]func(events *Journal){
		"RecordRace":         func(events *Journal) { events.RecordRace(widestOutcome) },
		"RecordTimeout":      func(events *Journal) { events.RecordTimeout(failedVote) },
		"RecordProbeTimeout": func(events *Journal) { events.RecordProbeTimeout(failedVote) },
		"GhostBurned": func(events *Journal) {
			events.GhostBurned("escrow-1", scheduler.Burn{Nonce: 7, Participant: hostAlpha, Reason: scheduler.GhostReasonAbandoned, RequestID: "request-1"})
		},
		"BurnBudgetExhausted": func(events *Journal) { events.BurnBudgetExhausted("escrow-1") },
		"RecordStep": func(events *Journal) {
			for _, step := range []engine.RaceStep{
				{Kind: engine.RaceStepNonceCommitted, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 7, Participant: hostAlpha, Slot: 1, Role: "primary", Reason: "primary"},
				{Kind: engine.RaceStepEscalationUnfilled, RequestID: "request-1", EscrowID: "escrow-1", Reason: "receipt_timeout", Attempts: 1, Err: failure},
				{Kind: engine.RaceStepAttemptCrowned, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 7, Participant: hostAlpha, Reason: "first_claim"},
				{Kind: engine.RaceStepAttemptFinished, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 8, Participant: hostBravo},
				{
					Kind: engine.RaceStepAttemptFinished, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 7, Participant: hostAlpha,
					Terminal: engine.TerminalWon, NonceFinished: true, HasOutcome: true, PhaseAborted: true, Outcome: widestAttempt,
				},
				{Kind: engine.RaceStepNonceStranded, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 9, Participant: hostBravo, Role: "speculative"},
				{Kind: engine.RaceStepHostBlocked, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 7, Participant: hostAlpha},
				{Kind: engine.RaceStepHostRewound, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 7, Participant: hostAlpha, Rewound: true},
			} {
				events.RecordStep(step)
			}
		},
		"RequestFinished": func(events *Journal) {
			events.RequestFinished(RequestLine{
				RequestID: "request-1", Model: "qwen", EscrowID: "escrow-1", ClientStream: true, Outcome: widestOutcome,
				Verdict: "failed_mid_stream", Bytes: 512, Elapsed: time.Second, RaceErr: failure, DeliverErr: failure,
			})
			events.RequestFinished(RequestLine{
				RequestID: "request-2", Model: "qwen", ClientStream: true,
				Outcome: engine.RaceOutcome{Attempts: []engine.AttemptOutcome{{Participant: hostAlpha}, {Participant: hostBravo}}},
				Verdict: "failed_before_first_byte", RaceErr: failure,
			})
			events.RequestFinished(RequestLine{RequestID: "request-3", Model: "qwen", EscrowID: "escrow-1", CacheHit: true, Bytes: 64})
		},
		"RequestThrottled": func(events *Journal) {
			events.RequestThrottled(RequestLine{RequestID: "request-4", Model: "qwen", LimiterReason: "queue_depth"})
		},
		"ReplyNotCached": func(events *Journal) {
			events.ReplyNotCached(RequestLine{RequestID: "request-5", Model: "qwen", EscrowID: "escrow-1"})
		},
		"DiffComposed":  func(events *Journal) { events.DiffComposed("escrow-1", &types.Diff{}) },
		"ProbeRecorded": func(events *Journal) { events.ProbeRecorded("escrow-1", accounting.Attempt{Nonce: 1, Sent: true}) },
		"HostWithheld": func(events *Journal) {
			events.HostWithheld(perf.Withholding{Participant: hostAlpha, Model: "qwen", Reason: "failure_rate", EjectionCount: 1, ConsecutiveFailures: 1, FailureRate: 0.5, FailureVolume: 20, WithheldFor: time.Minute})
		},
		"HostReturned":           func(events *Journal) { events.HostReturned(hostAlpha, "qwen", 1) },
		"HostContextLimit":       func(events *Journal) { events.HostContextLimit(hostAlpha, "qwen", 4096, 8192) },
		"HostToolsUnsupported":   func(events *Journal) { events.HostToolsUnsupported(hostAlpha, "qwen") },
		"HostVersionUnsupported": func(events *Journal) { events.HostVersionUnsupported(hostAlpha) },
		"HostCutOff": func(events *Journal) {
			events.HostCutOff(hostAlpha, "qwen", "consecutive_transport_faults", 1, 5*time.Second)
		},
		"HostCutOffLifted":      func(events *Journal) { events.HostCutOffLifted(hostAlpha, "qwen", 0) },
		"HostDeniedCrown":       func(events *Journal) { events.HostDeniedCrown(hostAlpha, "qwen", 3) },
		"HostServedUnsigned":    func(events *Journal) { events.HostServedUnsigned("7", "qwen", hostAlpha, 4) },
		"HostCrownedAgain":      func(events *Journal) { events.HostCrownedAgain(hostAlpha, "qwen") },
		"ExcludedHostServed":    func(events *Journal) { events.ExcludedHostServed("escrow-1", hostBravo) },
		"ForcedSend":            func(events *Journal) { events.ForcedSend("escrow-1", hostBravo, 6) },
		"EscrowServing":         func(events *Journal) { events.EscrowServing("7", "qwen") },
		"EscrowRetired":         func(events *Journal) { events.EscrowRetired("7") },
		"EscrowRetiredDraining": func(events *Journal) { events.EscrowRetiredDraining("7", 1) },
		"DrainingEscrowClosed": func(events *Journal) {
			events.DrainingEscrowClosed("7", nil)
			events.DrainingEscrowClosed("7", failure)
		},
		"SettlementUnverifiable": func(events *Journal) { events.SettlementUnverifiable("5", 9, failure) },
		"EscrowUnservable":       func(events *Journal) { events.EscrowUnservable("gone", failure) },
		"EscrowUnopened":         func(events *Journal) { events.EscrowUnopened("stalled", failure) },
		"EscrowCreated":          func(events *Journal) { events.EscrowCreated("42", "qwen", "temp", "bridge", 7, "TX") },
		"EscrowRecovered":        func(events *Journal) { events.EscrowRecovered("55", "qwen", "temp", 3, "TX") },
		"CommitmentCleared": func(events *Journal) {
			events.CommitmentCleared("TX", "qwen", "temp", 3, "transaction created no escrow")
		},
		"EscrowGoneFromChain":      func(events *Journal) { events.EscrowGoneFromChain("1") },
		"EscrowDeadlineReached":    func(events *Journal) { events.EscrowDeadlineReached("5", 7, 3100) },
		"EscrowDeadlineUnsettled":  func(events *Journal) { events.EscrowDeadlineUnsettled("5", 3100, "settlement_disabled") },
		"SettleMarginShort":        func(events *Journal) { events.SettleMarginShort(600, time.Second, 22*time.Minute) },
		"EscrowDeadlinesProjected": func(events *Journal) { events.EscrowDeadlinesProjected(2_000, 2_100, 10*time.Minute) },
		"EscrowChainFactsUnresolved": func(events *Journal) {
			events.EscrowChainFactsUnresolved("1", "lookup_failed")
		},
		"EscrowMarkedForReplacement": func(events *Journal) {
			events.EscrowMarkedForReplacement("1", "nonce_cap")
		},
		"EscrowCreateUnderfunded": func(events *Journal) { events.EscrowCreateUnderfunded("qwen", "reserve", 5, 101) },
		"EscrowCreateBelowFloor":  func(events *Journal) { events.EscrowCreateBelowFloor("qwen", "regular", 1000, 180010) },
		"EscrowReserveTaken":      func(events *Journal) { events.EscrowReserveTaken("1") },
		"RotationSkipped":         func(events *Journal) { events.RotationSkipped("qwen", "regular", 4) },
		"RegularsPromotedToTemp":  func(events *Journal) { events.RegularsPromotedToTemp("qwen", 9, 2) },
		"BridgePrepared":          func(events *Journal) { events.BridgePrepared("qwen", 9, 1, 2) },
		"BridgeFinished":          func(events *Journal) { events.BridgeFinished("qwen", 9, 2, 1) },
		"EscrowParked":            func(events *Journal) { events.EscrowParked("5") },
		"EscrowSettled":           func(events *Journal) { events.EscrowSettled("5", "qwen", "TX", "gonka1settler") },
		"SettlementReconciled":    func(events *Journal) { events.SettlementReconciled("7", "TX") },
		"SettledRecordDropped":    func(events *Journal) { events.SettledRecordDropped("5") },
		"EscrowTickFailed":        func(events *Journal) { events.EscrowTickFailed(failure) },
		"TimeoutsSwept":           func(events *Journal) { events.TimeoutsSwept(4, 3, 1) },
		"EscrowPlanned": func(events *Journal) {
			events.EscrowPlanned("qwen", 2, 1, "guard,idle_starved", false, 131_112, 974_444, 1)
		},
		"EscrowStarved":            func(events *Journal) { events.EscrowStarved("1", 40_000) },
		"EscrowFull":               func(events *Journal) { events.EscrowFull("1", 900_000) },
		"FundingGuaranteeBroken":   func(events *Journal) { events.FundingGuaranteeBroken("qwen", 1, 2) },
		"FundingMisconfigured":     func(events *Journal) { events.FundingMisconfigured("qwen", 69_000, 69_762) },
		"EscrowBudgetReached":      func(events *Journal) { events.EscrowBudgetReached("qwen", 6, 6) },
		"FundingPlanFailed":        func(events *Journal) { events.FundingPlanFailed(failure) },
		"RetirementPendingFlushed": func(events *Journal) { events.RetirementPendingFlushed("escrow-1", 3, failure) },
		"SettleBroadcast":          func(events *Journal) { events.SettleBroadcast("123", "TX", "gonka1settler") },
		"WarmupFoundNoNonce":       func(events *Journal) { events.WarmupFoundNoNonce("escrow-1", failure) },
		"EscrowWarmed":             func(events *Journal) { events.EscrowWarmed("escrow-1", "qwen", 7, false, failure) },
		"WarmupLedgerOpenFailed":   func(events *Journal) { events.WarmupLedgerOpenFailed("escrow-1", failure) },
		"WarmupVoted":              func(events *Journal) { events.WarmupVoted("escrow-1", 7, "failed", "timeout_collection_error") },
		"ChainEpoch":               func(events *Journal) { events.ChainEpoch(7, chain.EpochPhasePoCGenerate, 100, 150) },
		"ChainRequestsBlocked":     func(events *Journal) { events.ChainRequestsBlocked(chain.BlockReasonPoC, 7, 100) },
		"ChainRequestsUnblocked":   func(events *Journal) { events.ChainRequestsUnblocked(7, 120) },
		"ChainSnapshotStale":       func(events *Journal) { events.ChainSnapshotStale("fetch participants: 503", 7, 100) },
		"ChainSnapshotRecovered":   func(events *Journal) { events.ChainSnapshotRecovered(8, 200) },
	}
}

// Test flow:
//  1. Build the `everyProducer` sample table and list every method on `*Journal` via reflection.
//  2. Skip the methods listed in `methodsThatWriteNoLine`.
//  3. Assert every remaining method name appears as a key in the sample table.
func TestEveryProducerMethodIsSampled(t *testing.T) {
	samples := everyProducer()
	journalType := reflect.TypeFor[*Journal]()

	for index := range journalType.NumMethod() {
		name := journalType.Method(index).Name
		if slices.Contains(methodsThatWriteNoLine, name) {
			continue
		}
		if _, sampled := samples[name]; !sampled {
			t.Errorf("%s is not in everyProducer, so the keys it writes are never checked", name)
		}
	}
}

// Test flow:
//  1. Load the declared key vocabulary from `internal/logkey` via `declaredKeys`.
//  2. Build a journal with a `logcapture.Recorder` and a ledger that refuses every probe.
//  3. Drive every producer in `everyProducer`, then flush.
//  4. Assert every field key in every logged entry is a declared logkey value.
func TestEveryRenderedKeyIsDeclared(t *testing.T) {
	declared := declaredKeys(t)
	lines := &logcapture.Recorder{}
	events := newJournal(t, Settings{Lines: lines, Ledger: &ledgerSpy{probeRefusal: errors.New("probe refused")}})

	for _, produce := range everyProducer() {
		produce(events)
	}
	events.Flush()

	require.NotEmpty(t, lines.All())
	for _, entry := range lines.All() {
		for index := 0; index < len(entry.Fields); index += 2 {
			if key, isKey := entry.Fields[index].(string); !isKey || !declared[key] {
				t.Errorf("%q writes key %v, which internal/logkey does not declare", entry.Msg, entry.Fields[index])
			}
		}
	}
}

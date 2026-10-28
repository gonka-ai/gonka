package accounting

import (
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/scheduler"
)

// Constants, not configuration: two gateways must not report the same host differently. See docs/accounting.md.
const (
	findingMinimumVolume = 20

	executionTimeoutWarning  = 0.01
	executionTimeoutCritical = 0.05
	refusalWarning           = 0.05
	refusalCritical          = 0.20
	unusedAnswerWarning      = 0.20
	gatewayThrottleWarning   = 0.10
	stateDivergedWarning     = 0.01
	chainMissWarning         = 0.01
	chainMissCritical        = 0.05
	chainInvalidWarning      = 0.01
	chainInvalidCritical     = 0.05
	undecidedTimeoutWarning  = 0.10
	undecidedTimeoutCritical = 0.50
	unknownReasonWarning     = 0.05
	slowReceiptWarning       = 0.10
	slowChunkWarning         = 0.10
	clockDriftWarning        = 0.05
	slowDecodeWarning        = 0.10
	decodedLogprobsWarning   = 0.001
	decodedLogprobsCritical  = 0.01
	neverCritical            = 2.0

	expectedFindingsPerRecord = 4
)

// findingCodes is every code this gateway can emit, pinned so a rename has to be deliberate.
var findingCodes = []string{
	FindingExecutionTimeouts, FindingRefusals, FindingUnusedAnswers, FindingGatewayThrottled,
	FindingStateDiverged, FindingChainMisses, FindingChainInvalid, FindingUnresolvedChallenges,
	FindingUndecidedTimeouts, FindingUnknownReasons, FindingChainDisagreement, FindingLedgerOvercounted,
	FindingFailureTerminals, FindingSlowReceipts, FindingSlowChunks, FindingClockDrift, FindingSlowDecode,
	FindingDecodedLogprobs,
}

type Severity string

// A condition and the two numbers it was flagged on. What each code means lives in docs/accounting.md.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Part     uint64   `json:"part"`
	Whole    uint64   `json:"whole,omitempty"`
}

// Nonces that never reached the host are excluded from every rate: a burn is this gateway's own decision.
func findingsFor(record ParticipantRecord) []Finding {
	counted := countedOf(record)
	delivered := without(record.Dispositions[DispositionFinishedUsed]+
		record.Dispositions[DispositionFinishedUnused]+
		record.Dispositions[DispositionFinishedUsageUnknown], counted.deliveredWarmup)
	refused := without(record.Dispositions[DispositionUnfinishedRefused], counted.refusedOffRecord)
	unfinished := without(record.Dispositions[DispositionUnfinishedExecution], counted.unfinishedOffRecord)
	reached := delivered + refused + unfinished

	findings := make([]Finding, 0, expectedFindingsPerRecord)
	add := func(finding Finding, flagged bool) {
		if flagged {
			findings = append(findings, finding)
		}
	}
	add(ratio(unfinished, reached, executionTimeoutWarning, executionTimeoutCritical,
		FindingExecutionTimeouts))
	add(ratio(refused, reached, refusalWarning, refusalCritical,
		FindingRefusals))
	add(ratio(without(record.Dispositions[DispositionFinishedUnused], counted.unusedWarmup), delivered,
		unusedAnswerWarning, neverCritical, FindingUnusedAnswers))
	add(ratio(counted.throttledGhosts, record.Assigned, gatewayThrottleWarning, neverCritical,
		FindingGatewayThrottled))
	add(ratio(counted.stateDivergedGhosts, record.Assigned, stateDivergedWarning, neverCritical,
		FindingStateDiverged))
	add(ratio(uint64(record.ChainMissed), record.Assigned, chainMissWarning, chainMissCritical,
		FindingChainMisses))
	add(ratio(uint64(record.ChainInvalid), record.Assigned, chainInvalidWarning, chainInvalidCritical,
		FindingChainInvalid))
	add(ratio(record.UnresolvedChallenges, record.Assigned, chainInvalidWarning, chainInvalidCritical,
		FindingUnresolvedChallenges))
	add(ratio(undecidedTimeouts(record), timeoutRoundsVoted(record), undecidedTimeoutWarning, undecidedTimeoutCritical,
		FindingUndecidedTimeouts))
	add(ratio(record.UnknownReasonTotal, record.Assigned, unknownReasonWarning, neverCritical,
		FindingUnknownReasons))
	add(ratio(counted.slowReceiptsOutsidePoC, counted.acknowledgedOutsidePoC, slowReceiptWarning, neverCritical,
		FindingSlowReceipts))
	add(ratio(counted.slowChunksOutsidePoC, counted.deliveredOutsidePoC, slowChunkWarning, neverCritical,
		FindingSlowChunks))
	add(ratio(counted.slowDecodesOutsidePoC, counted.deliveredOutsidePoC, slowDecodeWarning, neverCritical,
		FindingSlowDecode))
	add(ratio(counted.logprobsDecoded, delivered, decodedLogprobsWarning, decodedLogprobsCritical,
		FindingDecodedLogprobs))
	add(ratio(counted.clockDrifted, delivered+record.Dispositions[DispositionUnfinishedExecution], clockDriftWarning, neverCritical,
		FindingClockDrift))

	if total := counted.failedWithoutAnswer; total > 0 && reached >= findingMinimumVolume {
		findings = append(findings, Finding{
			Code: FindingFailureTerminals, Severity: SeverityWarning, Part: total, Whole: reached,
		})
	}
	if record.Overcounted > 0 {
		findings = append(findings, Finding{
			Code: FindingLedgerOvercounted, Severity: SeverityWarning,
			Part: record.Overcounted, Whole: record.Assigned,
		})
	}
	if drift := record.CrossChecks.ErrorCount - record.Overcounted; drift > 0 && record.Assigned >= findingMinimumVolume {
		findings = append(findings, Finding{
			Code: FindingChainDisagreement, Severity: SeverityWarning,
			Part: drift, Whole: record.Assigned,
		})
	}
	return findings
}

// Every sum a rate below is measured from, so a participant's counters are walked once rather than per finding.
type countedNonces struct {
	deliveredWarmup        uint64
	unusedWarmup           uint64
	refusedOffRecord       uint64
	unfinishedOffRecord    uint64
	deliveredOutsidePoC    uint64
	acknowledgedOutsidePoC uint64
	slowReceiptsOutsidePoC uint64
	slowChunksOutsidePoC   uint64
	slowDecodesOutsidePoC  uint64
	logprobsDecoded        uint64
	clockDrifted           uint64
	failedWithoutAnswer    uint64
	throttledGhosts        uint64
	stateDivergedGhosts    uint64
}

func countedOf(record ParticipantRecord) countedNonces {
	var counted countedNonces
	for _, counter := range record.Counters {
		key, count := counter.CounterKey, counter.Count
		if key.LogprobsDecoded {
			counted.logprobsDecoded += count
		}
		if key.ClockDrifted {
			counted.clockDrifted += count
		}
		if outsidePoC(key) {
			if key.SlowReceipt {
				counted.slowReceiptsOutsidePoC += count
			}
			if key.SlowChunk {
				counted.slowChunksOutsidePoC += count
			}
			if key.SlowDecode {
				counted.slowDecodesOutsidePoC += count
			}
		}
		if failedWithoutAnswer(key) {
			counted.failedWithoutAnswer += count
		}
		switch key.Disposition {
		case DispositionFinishedUsed, DispositionFinishedUnused, DispositionFinishedUsageUnknown:
			if servedNoUser(key) {
				counted.deliveredWarmup += count
				if key.Disposition == DispositionFinishedUnused {
					counted.unusedWarmup += count
				}
			}
			if outsidePoC(key) {
				counted.deliveredOutsidePoC += count
				counted.acknowledgedOutsidePoC += count
			}
		case DispositionUnfinishedExecution:
			if offRecord(key) {
				counted.unfinishedOffRecord += count
			}
			if outsidePoC(key) {
				counted.acknowledgedOutsidePoC += count
			}
		case DispositionUnfinishedRefused:
			if offRecord(key) {
				counted.refusedOffRecord += count
			}
		case DispositionGhost:
			switch key.GhostReason {
			case scheduler.GhostReasonWindowFull, scheduler.GhostReasonCutOff, ghostReasonThrottledBeforeTheSplit:
				counted.throttledGhosts += count
			case scheduler.GhostReasonStateDiverged:
				counted.stateDivergedGhosts += count
			}
		}
	}
	return counted
}

func undecidedTimeouts(record ParticipantRecord) uint64 {
	return record.TimeoutOutcomes[TimeoutVoteCollectionFailed] + record.TimeoutOutcomes[TimeoutInsufficientVotes]
}

// A skipped round never asked anyone, so it cannot be undecided.
func timeoutRoundsVoted(record ParticipantRecord) uint64 {
	var total uint64
	for outcome, count := range record.TimeoutOutcomes {
		if outcome != TimeoutSkipped {
			total += count
		}
	}
	return total
}

// The denominator is taken once, so the rate and the numbers reported beside it cannot disagree.
func ratio(part, whole uint64, warning, critical float64, code string) (Finding, bool) {
	severity, flagged := rate(part, whole, warning, critical)
	if !flagged {
		return Finding{}, false
	}
	return Finding{Code: code, Severity: severity, Part: part, Whole: whole}, true
}

func rate(part, whole uint64, warning, critical float64) (Severity, bool) {
	if whole < findingMinimumVolume || part == 0 {
		return SeverityNone, false
	}
	measured := float64(part) / float64(whole)
	switch {
	case measured >= critical:
		return SeverityCritical, true
	case measured >= warning:
		return SeverityWarning, true
	}
	return SeverityNone, false
}

// The dispositions that reached the host and produced nothing; which terminal is in the counter beside it.
func failedWithoutAnswer(key CounterKey) bool {
	return isUnfinishedDisposition(key.Disposition)
}

func outsidePoC(key CounterKey) bool { return key.Phase != PhasePoC }

func servedNoUser(key CounterKey) bool { return key.Terminal == TerminalWarmupProbe }

// A warmup probe is the gateway's own request, so it leaves every rate whatever it ended as.
func offRecord(key CounterKey) bool { return excused(key) || servedNoUser(key) }

// A failure the host did not cause. Slowness that drove the client away is measured by its own findings.
func excused(key CounterKey) bool {
	if key.Terminal == TerminalClientCancelled || key.Terminal == engine.ReasonRequestTooLarge || key.Terminal == engine.ReasonEscrowLookupLimited {
		return true
	}
	switch key.TimeoutReason {
	case engine.TimeoutReasonLongResponse, engine.TimeoutReasonPhaseAborted,
		engine.TimeoutReasonCollectionError, engine.TimeoutReasonNotApplied, engine.TimeoutReasonNoPoster:
		return true
	}
	return false
}

func without(total, part uint64) uint64 {
	if part > total {
		return 0
	}
	return total - part
}

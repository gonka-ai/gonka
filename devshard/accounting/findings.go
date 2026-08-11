package accounting

// Thresholds are constants rather than configuration: a per-deployment one would let two gateways
// report the same host differently. neverCritical is the ceiling of a finding that stops at warning,
// since no rate exceeds 1.
const (
	findingMinimumVolume = 20

	executionTimeoutWarning  = 0.01
	executionTimeoutCritical = 0.05
	refusalWarning           = 0.05
	refusalCritical          = 0.20
	unusedAnswerWarning      = 0.20
	protocolMissWarning      = 0.01
	protocolMissCritical     = 0.05
	protocolInvalidWarning   = 0.01
	protocolInvalidCritical  = 0.05
	gatewayThrottleWarning   = 0.10
	quarantineWarning        = 0.10
	unknownReasonWarning     = 0.05
	slowReceiptWarning       = 0.05
	slowChunkWarning         = 0.05
	clockDriftWarning        = 0.01
	decodedLogprobsWarning   = 0.001
	decodedLogprobsCritical  = 0.01
	neverCritical            = 2.0
)

const (
	FindingExecutionTimeouts   = "execution_timeouts"
	FindingRefusals            = "refusals"
	FindingUnusedAnswers       = "answers_unused"
	FindingProtocolMisses      = "chain_recorded_misses"
	FindingProtocolInvalid     = "chain_recorded_invalid"
	FindingUnresolvedChallenge = "challenges_unresolved"
	FindingGatewayThrottled    = "throttled_by_gateway"
	FindingQuarantined         = "quarantined_by_gateway"
	FindingFailureOrigins      = "failure_origins"
	FindingChainDisagreement   = "ledger_disagrees_with_chain"
	FindingLedgerOvercounted   = "ledger_overcounted"
	FindingUnknownReasons      = "reasons_unknown"
	FindingDecodedLogprobs     = "logprobs_not_token_ids"
	FindingSlowReceipts        = "slow_receipts"
	FindingSlowChunks          = "slow_chunks"
	FindingClockDrift          = "clock_drift"
)

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Finding names a condition and the numbers it was flagged on, nothing more. What each code means and
// what to check lives in docs/accounting-findings.md, so an explanation is written once instead of
// crossing the network with every response.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Part     uint64   `json:"part"`
	Whole    uint64   `json:"whole,omitempty"` // zero when the finding counts rather than measures a rate
}

// findingsFor reads only what the record already carries, so a finding and the numbers beside it in
// the same response can never come from different reads of the ledger.
func findingsFor(record ParticipantRecord) []Finding {
	delivered := record.Dispositions[DispositionFinishedUsed] +
		record.Dispositions[DispositionFinishedUnused] +
		record.Dispositions[DispositionFinishedUsageUnknown]
	refused := countersWhere(record, both(is(DispositionUnfinishedRefused), blamesHost))
	unfinished := countersWhere(record, both(is(DispositionUnfinishedExecution), blamesHost))
	reached := delivered + refused + unfinished
	// The breakdown below names every failure, excused ones included, so it needs the denominator that
	// counts them too; the rates above measure only what the host is answerable for.
	reachedIncludingExcused := delivered +
		record.Dispositions[DispositionUnfinishedRefused] +
		record.Dispositions[DispositionUnfinishedExecution]

	findings := make([]Finding, 0, 4)
	add := func(finding Finding, flagged bool) {
		if flagged {
			findings = append(findings, finding)
		}
	}
	add(ratio(unfinished, reached, executionTimeoutWarning, executionTimeoutCritical,
		FindingExecutionTimeouts))
	add(ratio(refused, reached, refusalWarning, refusalCritical,
		FindingRefusals))
	add(ratio(record.Dispositions[DispositionFinishedUnused], delivered, unusedAnswerWarning, neverCritical,
		FindingUnusedAnswers))
	add(ratio(record.ProtocolMisses, record.AssignedNonces, protocolMissWarning, protocolMissCritical,
		FindingProtocolMisses))
	add(ratio(record.ProtocolInvalid, record.AssignedNonces, protocolInvalidWarning, protocolInvalidCritical,
		FindingProtocolInvalid))
	if record.UnresolvedChallenges > 0 {
		findings = append(findings, Finding{
			Code: FindingUnresolvedChallenge, Severity: SeverityWarning, Part: record.UnresolvedChallenges,
		})
	}
	add(ratio(ghostsBecause(record, NoSendParticipantThrottled), record.AssignedNonces, gatewayThrottleWarning, neverCritical,
		FindingGatewayThrottled))
	add(ratio(countersWhere(record, wasQuarantined), record.AssignedNonces, quarantineWarning, neverCritical,
		FindingQuarantined))
	add(ratio(record.UnknownReasonTotal, record.AssignedNonces, unknownReasonWarning, neverCritical,
		FindingUnknownReasons))

	add(ratio(countersWhere(record, receiptWasSlow), delivered+unfinished, slowReceiptWarning, neverCritical,
		FindingSlowReceipts))
	add(ratio(countersWhere(record, chunkWasSlow), delivered, slowChunkWarning, neverCritical,
		FindingSlowChunks))
	add(ratio(countersWhere(record, clockHasDrifted), delivered+unfinished, clockDriftWarning, neverCritical,
		FindingClockDrift))
	add(ratio(countersWhere(record, logprobsWereDecoded), delivered, decodedLogprobsWarning, decodedLogprobsCritical,
		FindingDecodedLogprobs))

	if total := countersWhere(record, failedWithoutAnswer); total > 0 && reachedIncludingExcused >= findingMinimumVolume {
		findings = append(findings, Finding{
			Code: FindingFailureOrigins, Severity: SeverityWarning, Part: total, Whole: reachedIncludingExcused,
		})
	}
	if drift := record.CrossChecks.ErrorCount - record.Overclassified; drift > 0 && record.AssignedNonces >= findingMinimumVolume {
		findings = append(findings, Finding{
			Code: FindingChainDisagreement, Severity: SeverityWarning, Part: drift, Whole: record.AssignedNonces,
		})
	}
	if record.Overclassified > 0 {
		findings = append(findings, Finding{
			Code: FindingLedgerOvercounted, Severity: SeverityWarning,
			Part: record.Overclassified, Whole: record.AssignedNonces,
		})
	}
	return findings
}

// ratio takes the denominator once, so the rate that decides and the numbers reported beside it cannot
// disagree about what they were measured against.
func ratio(part, whole uint64, warning, critical float64, code string) (Finding, bool) {
	severity, flagged := rate(part, whole, warning, critical)
	if !flagged {
		return Finding{}, false
	}
	return Finding{Code: code, Severity: severity, Part: part, Whole: whole}, true
}

func rate(part, whole uint64, warning, critical float64) (Severity, bool) {
	if whole < findingMinimumVolume || part == 0 {
		return "", false
	}
	measured := float64(part) / float64(whole)
	switch {
	case measured >= critical:
		return SeverityCritical, true
	case measured >= warning:
		return SeverityWarning, true
	}
	return "", false
}

func failedWithoutAnswer(key CounterKey) bool {
	return key.Disposition == DispositionUnfinishedRefused || key.Disposition == DispositionUnfinishedExecution
}

func logprobsWereDecoded(key CounterKey) bool { return key.LogprobsDecoded }
func receiptWasSlow(key CounterKey) bool      { return key.SlowReceipt }
func chunkWasSlow(key CounterKey) bool        { return key.SlowChunk }
func clockHasDrifted(key CounterKey) bool     { return key.ClockDrifted }

func countersWhere(record ParticipantRecord, match func(CounterKey) bool) uint64 {
	var total uint64
	for _, counter := range record.Counters {
		if match(counter.Key) {
			total += counter.Count
		}
	}
	return total
}

func both(first, second func(CounterKey) bool) func(CounterKey) bool {
	return func(key CounterKey) bool { return first(key) && second(key) }
}

func is(disposition Disposition) func(CounterKey) bool {
	return func(key CounterKey) bool { return key.Disposition == disposition }
}

// An origin the ledger could not name still counts against the host: treating "unknown" as excused
// would let every unclassified failure disappear from the rates.
func blamesHost(key CounterKey) bool {
	return key.FailureOrigin != FailureGatewayPolicy && key.FailureOrigin != FailureClient
}

func wasQuarantined(key CounterKey) bool {
	return key.QuarantineMode != "" && key.QuarantineMode != QuarantineNone
}

func ghostsBecause(record ParticipantRecord, reason NoSendReason) uint64 {
	var total uint64
	for _, counter := range record.Counters {
		if counter.Key.Disposition == DispositionGhost && counter.Key.NoSendReason == reason {
			total += counter.Count
		}
	}
	return total
}

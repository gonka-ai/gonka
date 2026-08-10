package accounting

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

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
)

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Observed string   `json:"observed"`
	Detail   string   `json:"detail"`
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
		FindingExecutionTimeouts, "nonces were acknowledged and never finished",
		"Accepted the work and delivered nothing, so the nonce is held to the execution deadline "+
			"instead of freed at the refusal one. Check the output length against the host's decode rate."))
	add(ratio(refused, reached, refusalWarning, refusalCritical,
		FindingRefusals, "nonces were never acknowledged",
		"The host did not take the work. Cheaper than a timeout but still spends the nonce; points at "+
			"capacity or reachability rather than speed."))
	add(ratio(record.Dispositions[DispositionFinishedUnused], delivered, unusedAnswerWarning, neverCritical,
		FindingUnusedAnswers, "finished answers were not used",
		"Finished after another host had already answered. A throughput problem, not an availability one."))
	add(ratio(record.ProtocolMisses, record.AssignedNonces, protocolMissWarning, protocolMissCritical,
		FindingProtocolMisses, "assigned nonces were recorded as missed on chain",
		"The chain's own verdict, from settled host statistics — the number that costs the host its "+
			"reward. It should track the applied timeouts above."))
	add(ratio(record.ProtocolInvalid, record.AssignedNonces, protocolInvalidWarning, protocolInvalidCritical,
		FindingProtocolInvalid, "assigned nonces were invalidated on chain",
		"A validator replayed the work and got a different answer. Not about speed — check the model "+
			"and runtime version the host serves."))
	if record.UnresolvedChallenges > 0 {
		findings = append(findings, Finding{
			Code:     FindingUnresolvedChallenge,
			Severity: SeverityWarning,
			Observed: fmt.Sprintf("%d challenges have no verdict yet", record.UnresolvedChallenges),
			Detail: "A dispute with no verdict yet. Until it resolves the nonce counts as neither valid " +
				"nor invalid.",
		})
	}
	add(ratio(ghostsBecause(record, NoSendParticipantThrottled), record.AssignedNonces, gatewayThrottleWarning, neverCritical,
		FindingGatewayThrottled, "assigned nonces were burned without being sent",
		"Our decision, not the host's failure. The per-host window narrows after failures and widens "+
			"as they stop, so this trails the other findings."))
	add(ratio(countersWhere(record, wasQuarantined), record.AssignedNonces, quarantineWarning, neverCritical,
		FindingQuarantined, "assigned nonces were handled under quarantine",
		"Our reaction too: the host was being probed, shadowed, or held on probation, so these nonces "+
			"were not served the way a healthy host's are."))
	add(ratio(record.UnknownReasonTotal, record.AssignedNonces, unknownReasonWarning, neverCritical,
		FindingUnknownReasons, "classified nonces carry a reason this ledger could not name",
		"A gap in this gateway's instrumentation, not a host fault: a terminal state reached through a "+
			"path the ledger cannot name."))

	if total, breakdown := failureOrigins(record); total > 0 && reachedIncludingExcused >= findingMinimumVolume {
		findings = append(findings, Finding{
			Code:     FindingFailureOrigins,
			Severity: SeverityWarning,
			Observed: fmt.Sprintf("%d of %d nonces reached this participant and produced no usable answer: %s",
				total, reachedIncludingExcused, breakdown),
			Detail: "Who ended each failure. Only host_response is the host's; gateway_policy and client " +
				"are already excluded from the rates above. A failure during PoC is expected.",
		})
	}
	// The two halves of the cross-check are not the same kind of number. Drift between this gateway
	// and the chain is expected while an escrow is live, so it waits for volume; a ledger that counted
	// more nonces than the slot was ever given is a broken invariant at any volume.
	if drift := record.CrossChecks.ErrorCount - record.Overclassified; drift > 0 && record.AssignedNonces >= findingMinimumVolume {
		findings = append(findings, Finding{
			Code:     FindingChainDisagreement,
			Severity: SeverityWarning,
			Observed: fmt.Sprintf("%d nonces the ledger and the chain disagree about: %d applied timeouts against %d chain misses, %d recorded invalid against %d chain invalid",
				drift, record.CrossChecks.TimeoutApplied, record.CrossChecks.HostMissed,
				record.CrossChecks.RecordedInvalid, record.CrossChecks.HostInvalid),
			Detail: "Expected drift while an escrow is live, and it converges on its own. A gap that " +
				"survives settlement means one of the two is wrong.",
		})
	}
	if record.Overclassified > 0 {
		findings = append(findings, Finding{
			Code:     FindingLedgerOvercounted,
			Severity: SeverityWarning,
			Observed: fmt.Sprintf("%d beyond the %d nonces the chain assigned", record.Overclassified, record.AssignedNonces),
			Detail: "More nonces than the chain assigned, so one of the two is wrong. No host behaviour " +
				"produces this — report it.",
		})
	}
	return findings
}

// ratio takes the denominator once, so the rate that decides and the numbers that explain it cannot
// disagree about what they were measured against.
func ratio(part, whole uint64, warning, critical float64, code, what, detail string) (Finding, bool) {
	severity, flagged := rate(part, whole, warning, critical)
	if !flagged {
		return Finding{}, false
	}
	return Finding{Code: code, Severity: severity, Observed: observed(part, whole, what), Detail: detail}, true
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

func observed(part, whole uint64, what string) string {
	return fmt.Sprintf("%d of %d %s (%.1f%%)", part, whole, what, 100*float64(part)/float64(whole))
}

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

func failureOrigins(record ParticipantRecord) (uint64, string) {
	counts := make(map[string]uint64)
	var total uint64
	for _, counter := range record.Counters {
		if counter.Key.Disposition != DispositionUnfinishedRefused &&
			counter.Key.Disposition != DispositionUnfinishedExecution {
			continue
		}
		label := string(counter.Key.FailureOrigin)
		if label == "" {
			label = string(FailureTransportUnknown)
		}
		if counter.Key.DispatchPhase == PhasePoC || counter.Key.TimeoutEvaluationPhase == PhasePoC {
			label += " during PoC"
		}
		counts[label] += counter.Count
		total += counter.Count
	}
	ordered := slices.SortedFunc(maps.Keys(counts), func(left, right string) int {
		if counts[left] != counts[right] {
			return cmp.Compare(counts[right], counts[left])
		}
		return strings.Compare(left, right)
	})
	described := make([]string, 0, len(ordered))
	for _, label := range ordered {
		described = append(described, fmt.Sprintf("%s (%d)", label, counts[label]))
	}
	return total, strings.Join(described, ", ")
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

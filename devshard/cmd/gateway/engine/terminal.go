package engine

import (
	"net/http"

	"devshard/cmd/gateway/limits"
)

// Terminal is an attempt's classified end state; every downstream vocabulary is a total function of it. See race.md, "The outcome".
type Terminal int

const (
	TerminalUnclassified Terminal = iota
	TerminalWon
	TerminalLost
	TerminalThrottled
	TerminalUnavailable
	TerminalForbidden
	TerminalNotFound
	TerminalTimestampDrift
	TerminalRejected
	TerminalUpstreamServerError
	TerminalOffPath
	TerminalDialFailure
	TerminalStreamTruncated
	TerminalUnexpectedEOF
	TerminalResponseTooLarge
	TerminalClientCancelled
	TerminalNoReceipt
	TerminalEmptyStream
	TerminalBurnEmpty
	TerminalErrorStream
	TerminalCapabilityRefused
	TerminalRequestTooLarge
	TerminalStalled
	TerminalHardTimeout
	TerminalEscrowLookupLimited
	terminalCount
)

// See README.md, "Outcome, samples and verdicts".
var (
	terminalStatuses = map[Terminal]int{
		TerminalThrottled:      http.StatusTooManyRequests,
		TerminalUnavailable:    http.StatusServiceUnavailable,
		TerminalForbidden:      http.StatusForbidden,
		TerminalNotFound:       http.StatusNotFound,
		TerminalTimestampDrift: http.StatusUnauthorized,
	}

	terminalForStatus = func() map[int]Terminal {
		inverse := make(map[int]Terminal, len(terminalStatuses))
		for terminal, status := range terminalStatuses {
			inverse[status] = terminal
		}
		return inverse
	}()
)

// terminalTraits is what the failure reason and the limiter verdict read from a terminal; an empty reason is a win or a loss.
type terminalTraits struct {
	reason     string
	verdict    limits.Verdict
	hasVerdict bool
}

var terminalTable = [terminalCount]terminalTraits{
	TerminalUnclassified:        {reason: ReasonUnknown},
	TerminalWon:                 {verdict: limits.Success, hasVerdict: true},
	TerminalLost:                {verdict: limits.Success, hasVerdict: true},
	TerminalThrottled:           {reason: ReasonThrottled, verdict: limits.Overload, hasVerdict: true},
	TerminalUnavailable:         {reason: ReasonUnavailable, verdict: limits.Overload, hasVerdict: true},
	TerminalForbidden:           {reason: ReasonForbidden, verdict: limits.TransportFault, hasVerdict: true},
	TerminalNotFound:            {reason: ReasonNotFound, verdict: limits.TransportFault, hasVerdict: true},
	TerminalTimestampDrift:      {reason: ReasonTimestampDrift, verdict: limits.TransportFault, hasVerdict: true},
	TerminalRejected:            {reason: ReasonRejected},
	TerminalUpstreamServerError: {reason: ReasonUpstreamServer, verdict: limits.UpstreamFault, hasVerdict: true},
	TerminalOffPath:             {reason: ReasonOffPath},
	TerminalDialFailure:         {reason: ReasonDialFailure, verdict: limits.TransportFault, hasVerdict: true},
	TerminalStreamTruncated:     {reason: ReasonStreamTruncated, verdict: limits.TransportFault, hasVerdict: true},
	TerminalUnexpectedEOF:       {reason: ReasonUnexpectedEOF, verdict: limits.TransportFault, hasVerdict: true},
	TerminalResponseTooLarge:    {reason: ReasonResponseTooLarge, verdict: limits.ModelOutcome, hasVerdict: true},
	TerminalClientCancelled:     {reason: ReasonClientCancelled},
	TerminalNoReceipt:           {reason: ReasonNoReceipt},
	TerminalEmptyStream:         {reason: ReasonEmptyStream, verdict: limits.ModelOutcome, hasVerdict: true},
	TerminalBurnEmpty:           {reason: ReasonEmptyStream, verdict: limits.ModelOutcome, hasVerdict: true},
	TerminalErrorStream:         {reason: ReasonErrorStream, verdict: limits.ModelOutcome, hasVerdict: true},
	TerminalCapabilityRefused:   {reason: ReasonErrorStream, verdict: limits.ModelOutcome, hasVerdict: true},
	TerminalRequestTooLarge:     {reason: ReasonRequestTooLarge, verdict: limits.ModelOutcome, hasVerdict: true},
	TerminalStalled:             {reason: ReasonStalled, verdict: limits.DecodeStalled, hasVerdict: true},
	TerminalHardTimeout:         {reason: ReasonHardTimeout, verdict: limits.Overload, hasVerdict: true},
	TerminalEscrowLookupLimited: {reason: ReasonEscrowLookupLimited, verdict: limits.ModelOutcome, hasVerdict: true},
}

// StatusFor reports the upstream status a terminal was recovered from, false for those that carried none.
func StatusFor(terminal Terminal) (int, bool) {
	status, known := terminalStatuses[terminal]
	return status, known
}

func (t Terminal) verdict() (limits.Verdict, bool) {
	if traits := t.traits(); traits.hasVerdict {
		return traits.verdict, true
	}
	return limits.ModelOutcome, false
}

// String names the terminal, taking failure names from reason() rather than a second list.
func (t Terminal) String() string {
	switch t {
	case TerminalWon:
		return TerminalNameWon
	case TerminalLost:
		return TerminalNameLost
	case TerminalUnclassified:
		return TerminalNameUnclassified
	}
	if reason := t.reason(); reason != "" {
		return reason
	}
	return TerminalNameUnnamed
}

func (t Terminal) reason() string { return t.traits().reason }

func (t Terminal) traits() terminalTraits {
	if t < 0 || t >= terminalCount {
		return terminalTraits{reason: ReasonUnknown}
	}
	return terminalTable[t]
}

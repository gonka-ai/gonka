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

// StatusFor reports the upstream status a terminal was recovered from, false for those that carried none.
func StatusFor(terminal Terminal) (int, bool) {
	status, known := terminalStatuses[terminal]
	return status, known
}

func (t Terminal) verdict() (limits.Verdict, bool) {
	switch t {
	case TerminalWon, TerminalLost:
		return limits.Success, true
	case TerminalThrottled, TerminalUnavailable, TerminalHardTimeout:
		return limits.Overload, true
	case TerminalUpstreamServerError:
		return limits.UpstreamFault, true
	case TerminalForbidden, TerminalNotFound, TerminalTimestampDrift,
		TerminalDialFailure, TerminalStreamTruncated, TerminalUnexpectedEOF:
		return limits.TransportFault, true
	case TerminalStalled:
		return limits.DecodeStalled, true
	case TerminalEmptyStream, TerminalBurnEmpty, TerminalErrorStream, TerminalCapabilityRefused,
		TerminalResponseTooLarge, TerminalRequestTooLarge, TerminalEscrowLookupLimited:
		return limits.ModelOutcome, true
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

func (t Terminal) reason() string {
	switch t {
	case TerminalWon, TerminalLost:
		return ""
	case TerminalThrottled:
		return ReasonThrottled
	case TerminalUnavailable:
		return ReasonUnavailable
	case TerminalForbidden:
		return ReasonForbidden
	case TerminalNotFound:
		return ReasonNotFound
	case TerminalTimestampDrift:
		return ReasonTimestampDrift
	case TerminalRejected:
		return ReasonRejected
	case TerminalUpstreamServerError:
		return ReasonUpstreamServer
	case TerminalOffPath:
		return ReasonOffPath
	case TerminalDialFailure:
		return ReasonDialFailure
	case TerminalStreamTruncated:
		return ReasonStreamTruncated
	case TerminalUnexpectedEOF:
		return ReasonUnexpectedEOF
	case TerminalRequestTooLarge:
		return ReasonRequestTooLarge
	case TerminalEscrowLookupLimited:
		return ReasonEscrowLookupLimited
	case TerminalResponseTooLarge:
		return ReasonResponseTooLarge
	case TerminalClientCancelled:
		return ReasonClientCancelled
	case TerminalNoReceipt:
		return ReasonNoReceipt
	case TerminalEmptyStream, TerminalBurnEmpty:
		return ReasonEmptyStream
	case TerminalErrorStream, TerminalCapabilityRefused:
		return ReasonErrorStream
	case TerminalStalled:
		return ReasonStalled
	case TerminalHardTimeout:
		return ReasonHardTimeout
	}
	return ReasonUnknown
}

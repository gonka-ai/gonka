package journal

import (
	"time"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/perf"
)

// HostWithheld renders perf taking a host out of routing. See README.md, "Lifecycle lines outside a race".
func (j *Journal) HostWithheld(withheld perf.Withholding) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Warn("host withheld from routing",
			logkey.Host, logkey.ShortHost(withheld.Participant), logkey.Model, withheld.Model,
			logkey.Reason, withheld.Reason,
			logkey.EjectionCount, withheld.EjectionCount,
			logkey.ConsecutiveFailures, withheld.ConsecutiveFailures,
			logkey.FailureRate, withheld.FailureRate, logkey.FailureVolume, withheld.FailureVolume,
			logkey.WithheldForMS, withheld.WithheldFor.Milliseconds())
	})
}

// HostReturned renders the first sample after a withholding lapsed.
func (j *Journal) HostReturned(participant, model string, ejectionCount int) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Info("host back in routing",
			logkey.Host, logkey.ShortHost(participant), logkey.Model, model,
			logkey.EjectionCount, ejectionCount)
	})
}

// HostContextLimit renders a host admitting a context length smaller than any before.
func (j *Journal) HostContextLimit(participant, model string, contextLimit, previousContextLimit uint64) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Info("host admitted a context length it will not exceed", logkey.Host, logkey.ShortHost(participant),
			logkey.Model, model, logkey.ContextLimit, contextLimit, logkey.PreviousContextLimit, previousContextLimit)
	})
}

// HostToolsUnsupported renders a host build's first tool-call refusal.
func (j *Journal) HostToolsUnsupported(participant, model string) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Info("host build does not implement tool calling", logkey.Host, logkey.ShortHost(participant),
			logkey.Model, model)
	})
}

// HostVersionUnsupported renders a host build's first protocol-version refusal.
func (j *Journal) HostVersionUnsupported(participant string) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Info("host build cannot serve the escrow's protocol version", logkey.Host, logkey.ShortHost(participant))
	})
}

// HostCutOff renders the participant limiter opening a host's breaker.
func (j *Journal) HostCutOff(participant, model, reason string, backoffCount int, cutOffFor time.Duration) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Warn("host cut off after transport faults",
			logkey.Host, logkey.ShortHost(participant), logkey.Model, model,
			logkey.Reason, reason, logkey.BackoffCount, backoffCount,
			logkey.CutOffForMS, cutOffFor.Milliseconds())
	})
}

// HostCutOffLifted renders a half-open probe that answered.
func (j *Journal) HostCutOffLifted(participant, model string, backoffCount int) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Info("host back after its cut-off",
			logkey.Host, logkey.ShortHost(participant), logkey.Model, model,
			logkey.BackoffCount, backoffCount)
	})
}

// HostServedUnsigned renders a host whose served stream matches neither hash of the Finish it signed.
func (j *Journal) HostServedUnsigned(escrowID, model, participant string, nonce uint64) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Warn("served stream does not match its finish",
			logkey.Escrow, escrowID, logkey.Nonce, nonce, logkey.Host, logkey.ShortHost(participant), logkey.Model, model)
	})
}

// HostDeniedCrown renders the strike that withholds the crown from a host.
func (j *Journal) HostDeniedCrown(participant, model string, strikes int) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Warn("host denied the crown",
			logkey.Host, logkey.ShortHost(participant), logkey.Model, model,
			logkey.Strikes, strikes)
	})
}

// HostCrownedAgain renders the answer with content that restores a host's crown.
func (j *Journal) HostCrownedAgain(participant, model string) {
	j.emitLine(KindHostTransition, func(lines logSink) {
		lines.Info("host crowned again",
			logkey.Host, logkey.ShortHost(participant), logkey.Model, model)
	})
}

// ExcludedHostServed renders a nonce spent on a host its request excluded, once the match wait had passed.
func (j *Journal) ExcludedHostServed(escrowID, participant string) {
	j.emitLine(KindExcludedHostServed, func(lines logSink) {
		lines.Info("nonce spent on a host the request excluded", logkey.Escrow, escrowID,
			logkey.Host, logkey.ShortHost(participant))
	})
}

// ForcedSend renders a nonce sent over a host's full congestion window, which the run of burns before it bought.
func (j *Journal) ForcedSend(escrowID, participant string, burnsInARow int64) {
	j.emitLine(KindForcedSend, func(lines logSink) {
		lines.Info("nonce sent over a full congestion window", logkey.Escrow, escrowID,
			logkey.Host, logkey.ShortHost(participant), logkey.BurnsInARow, burnsInARow)
	})
}

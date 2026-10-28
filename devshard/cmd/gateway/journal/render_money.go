package journal

import (
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/scheduler"
)

const (
	burnFieldSlots        = 10
	timeoutVoteFieldSlots = 14
)

// renderBurn names the nonce only when the session committed one (README.md, "Absent subjects are omitted") and the request only when the burn had one (see "Which request a burn and a vote belong to").
func renderBurn(lines logSink, escrowID string, burned scheduler.Burn) {
	fields := make([]any, 0, burnFieldSlots)
	fields = append(fields, logkey.Escrow, escrowID)
	if burned.Nonce != 0 {
		fields = append(fields, logkey.Nonce, burned.Nonce)
	}
	fields = append(fields, logkey.Host, logkey.ShortHost(burned.Participant), logkey.Reason, burned.Reason)
	if burned.RequestID != "" {
		fields = append(fields, logkey.BurnedDuringRequest, burned.RequestID)
	}
	lines.Warn("nonce burned for nobody", fields...)
}

func renderBurnBudgetExhausted(lines logSink, escrowID string) {
	lines.Warn("escrow stopped burning nonces at its budget", logkey.Escrow, escrowID)
}

// renderTimeoutVote reports a vote that never reached the chain, naming its request when it has one. See race.md, "Timeout votes".
func renderTimeoutVote(lines logSink, vote *engine.TimeoutEvent) {
	if vote.Action != engine.TimeoutActionFailed || vote.Reason == engine.TimeoutReasonEscrowGone {
		return
	}
	fields := make([]any, 0, timeoutVoteFieldSlots)
	if vote.RequestID != "" {
		fields = append(fields, logkey.Request, vote.RequestID)
	}
	fields = append(fields,
		logkey.Escrow, vote.EscrowID, logkey.Nonce, vote.Nonce,
		logkey.Host, logkey.ShortHost(vote.Participant), logkey.Model, vote.Model,
		logkey.Kind, vote.Kind, logkey.Reason, vote.Reason)
	lines.Warn("timeout vote failed", fields...)
}

// renderProbeRefused names the warmup nonce the ledger refused; nothing else records it.
func renderProbeRefused(lines logSink, escrowID string, nonce uint64, err error) {
	lines.Warn("escrow warmup could not settle its nonce", logkey.Escrow, escrowID, logkey.Nonce, nonce, logkey.Error, err)
}

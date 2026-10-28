package journal

import (
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logkey"
)

// WarmupFoundNoNonce renders a probe that committed nothing, naming its error only when it had one. See README.md, "Nil errors are omitted".
func (j *Journal) WarmupFoundNoNonce(escrowID string, probeErr error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		fields := []any{logkey.Escrow, escrowID}
		if probeErr != nil {
			fields = append(fields, logkey.Error, probeErr)
		}
		lines.Warn("escrow warmup found no nonce to spend", fields...)
	})
}

// EscrowWarmed renders a probe that spent its nonce, naming a catch-up failure only when there was one.
func (j *Journal) EscrowWarmed(escrowID, model string, nonce uint64, served bool, catchUpErr error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		fields := []any{logkey.Escrow, escrowID, logkey.Model, model, logkey.Nonce, nonce, logkey.Served, served}
		if catchUpErr != nil {
			fields = append(fields, logkey.CatchUpError, catchUpErr)
		}
		lines.Info("escrow warmed", fields...)
	})
}

// WarmupLedgerOpenFailed renders a ledger that refused the escrow the probe is about to be recorded against.
func (j *Journal) WarmupLedgerOpenFailed(escrowID string, err error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow warmup could not open the escrow in the ledger", logkey.Escrow, escrowID, logkey.Error, err)
	})
}

// WarmupVoted renders the probe's own timeout vote, at Warn when it failed, as a race's failed vote is.
func (j *Journal) WarmupVoted(escrowID string, nonce uint64, action, reason string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		fields := []any{logkey.Escrow, escrowID, logkey.Nonce, nonce, logkey.Action, action, logkey.Reason, reason}
		if action == engine.TimeoutActionFailed {
			lines.Warn("escrow warmup voted on its unfinished nonce", fields...)
			return
		}
		lines.Info("escrow warmup voted on its unfinished nonce", fields...)
	})
}

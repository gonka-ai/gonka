package journal

import (
	"time"

	"devshard/cmd/gateway/internal/logkey"
)

// EscrowServing renders an escrow published for routing. See README.md, "Lifecycle lines outside a race".
func (j *Journal) EscrowServing(escrowID, model string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow serving", logkey.Escrow, escrowID, logkey.Model, model)
	})
}

// EscrowRetired renders an idle escrow taken out of routing.
func (j *Journal) EscrowRetired(escrowID string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow retired", logkey.Escrow, escrowID)
	})
}

// EscrowRetiredDraining renders an escrow taken out of routing with requests still running on it.
func (j *Journal) EscrowRetiredDraining(escrowID string, inFlight int64) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow retired, draining", logkey.Escrow, escrowID, logkey.InFlight, inFlight)
	})
}

// DrainingEscrowClosed renders a drained escrow's close, or the failure that keeps its storage held.
func (j *Journal) DrainingEscrowClosed(escrowID string, closeErr error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		if closeErr != nil {
			lines.Error("draining escrow failed to close, its storage stays held", logkey.Escrow, escrowID, logkey.Error, closeErr)
			return
		}
		lines.Info("draining escrow closed", logkey.Escrow, escrowID)
	})
}

// RetirementPendingFlushed renders the last diff a retiring escrow carried its gossiped transactions in.
func (j *Journal) RetirementPendingFlushed(escrowID string, pending int, err error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		if err != nil {
			lines.Error("retiring escrow could not carry its pending transactions",
				logkey.Escrow, escrowID, logkey.PendingTxs, pending, logkey.Error, err)
			return
		}
		lines.Info("retiring escrow carried its pending transactions",
			logkey.Escrow, escrowID, logkey.PendingTxs, pending)
	})
}

// SettlementUnverifiable renders a settlement payload the chain would refuse, built anyway.
func (j *Journal) SettlementUnverifiable(escrowID string, nonce uint64, unverifiable error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("settlement signatures did not verify",
			logkey.Escrow, escrowID, logkey.Nonce, nonce, logkey.Error, unverifiable)
	})
}

// EscrowUnservable renders a stored devshard boot marks inactive because the chain or the environment cannot serve it.
func (j *Journal) EscrowUnservable(escrowID string, err error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("devshard cannot be served, marking inactive", logkey.Escrow, escrowID, logkey.Error, err)
	})
}

// EscrowUnopened renders an active devshard whose session would not open; it stays unpublished and the republish retries it.
func (j *Journal) EscrowUnopened(escrowID string, err error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("devshard could not be opened, retrying", logkey.Escrow, escrowID, logkey.Error, err)
	})
}

// EscrowCreated renders funds committed to a new escrow and why it was created; the id is text like every other escrow id.
func (j *Journal) EscrowCreated(escrowID, model, role, reason string, epoch uint64, txHash string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow created", logkey.Escrow, escrowID, logkey.Model, model, logkey.Role, role, logkey.Reason, reason, logkey.Epoch, epoch, logkey.Tx, txHash)
	})
}

// EscrowCreateUnderfunded renders a create refused before broadcast because the wallet cannot pay for it.
func (j *Journal) EscrowCreateUnderfunded(model, role string, have, need uint64) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow create refused: wallet underfunded", logkey.Model, model, logkey.Role, role, logkey.Have, have, logkey.Need, need)
	})
}

// EscrowCreateBelowFloor renders a create refused before broadcast because the model's amount cannot pay one full-context request.
func (j *Journal) EscrowCreateBelowFloor(model, role string, amount, floor uint64) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow create refused: amount below the model's floor", logkey.Model, model, logkey.Role, role, logkey.Have, amount, logkey.Need, floor)
	})
}

// EscrowReserveTaken renders a reserve escrow a request needed; it serves as a regular escrow from now on.
func (j *Journal) EscrowReserveTaken(escrowID string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("reserve escrow taken", logkey.Escrow, escrowID)
	})
}

// EscrowRecovered renders a create that landed while the gateway was down.
func (j *Journal) EscrowRecovered(escrowID, model, role string, epoch uint64, txHash string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow recovered from commitment", logkey.Escrow, escrowID, logkey.Model, model, logkey.Role, role, logkey.Epoch, epoch, logkey.Tx, txHash)
	})
}

// CommitmentCleared renders an abandoned creation intent and why.
func (j *Journal) CommitmentCleared(txHash, model, role string, epoch uint64, reason string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("commitment cleared", logkey.Tx, txHash, logkey.Model, model, logkey.Role, role, logkey.Epoch, epoch, logkey.Reason, reason)
	})
}

// EscrowGoneFromChain renders a confirmed not-found taking an escrow out of service, or recording a row already out of it as lost.
func (j *Journal) EscrowGoneFromChain(escrowID string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow gone from chain, taken out of service", logkey.Escrow, escrowID)
	})
}

// EscrowDeadlineReached renders an escrow parked because its chain epoch's settlement window is closing.
func (j *Journal) EscrowDeadlineReached(escrowID string, chainEpoch uint64, settleBy int64) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		fields := withSettleBy([]any{logkey.Escrow, escrowID, logkey.ChainEpoch, chainEpoch}, settleBy)
		lines.Warn("escrow parked at its settlement deadline", fields...)
	})
}

// EscrowDeadlineUnsettled renders an escrow whose settlement window closes with nothing allowed to settle it.
func (j *Journal) EscrowDeadlineUnsettled(escrowID string, settleBy int64, reason string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		fields := withSettleBy([]any{logkey.Escrow, escrowID}, settleBy)
		lines.Error("escrow deadline passes unsettled", append(fields, logkey.Reason, reason)...)
	})
}

// EscrowDeadlinesProjected renders the first tick of an episode in which settle deadlines are read past a chain snapshot older than its max age.
func (j *Journal) EscrowDeadlinesProjected(height, projected int64, staleFor time.Duration) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow deadlines read past a stale chain height", logkey.Height, height, logkey.ProjectedHeight, projected, logkey.StaleForMS, staleFor.Milliseconds())
	})
}

func withSettleBy(fields []any, settleBy int64) []any {
	if settleBy <= 0 {
		return fields
	}
	return append(fields, logkey.SettleBy, settleBy)
}

// SettleMarginShort renders a settle margin the measured block time makes shorter than two settle-transaction windows.
func (j *Journal) SettleMarginShort(marginBlocks int64, blockTime, need time.Duration) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("settle margin shorter than two settle windows",
			logkey.MarginBlocks, marginBlocks, logkey.BlockTimeMS, blockTime.Milliseconds(), logkey.SettleWindowsMS, need.Milliseconds())
	})
}

// EscrowChainFactsUnresolved renders the first failed read of an escrow's chain epoch and amount; it is not repeated until the row resolves.
func (j *Journal) EscrowChainFactsUnresolved(escrowID, reason string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow chain epoch and amount unresolved", logkey.Escrow, escrowID, logkey.Reason, reason)
	})
}

// EscrowMarkedForReplacement renders the first mark on an exhausted escrow.
func (j *Journal) EscrowMarkedForReplacement(escrowID, reason string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("escrow marked for replacement", logkey.Escrow, escrowID, logkey.Reason, reason)
	})
}

// RotationSkipped renders a rotation that created nothing because the network serves no such model.
func (j *Journal) RotationSkipped(model, role string, epoch uint64) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("rotation skipped, the network serves no such model",
			logkey.Model, model, logkey.Role, role, logkey.Epoch, epoch)
	})
}

// RegularsPromotedToTemp renders the bridge's degrade path.
func (j *Journal) RegularsPromotedToTemp(model string, epoch uint64, promoted int) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Warn("regular escrows promoted to temp", logkey.Model, model, logkey.Epoch, epoch, logkey.Promoted, promoted)
	})
}

// BridgePrepared renders the temp escrows swapped in ahead of an epoch switch.
func (j *Journal) BridgePrepared(model string, epoch uint64, created, retired int) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("bridge prepared", logkey.Model, model, logkey.Epoch, epoch, logkey.Created, created, logkey.Retired, retired)
	})
}

// BridgeFinished renders the regular escrows swapped back in after proof-of-compute.
func (j *Journal) BridgeFinished(model string, epoch uint64, created, retired int) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("bridge finished", logkey.Model, model, logkey.Epoch, epoch, logkey.Created, created, logkey.Retired, retired)
	})
}

// EscrowParked renders an escrow taken out of routing to be settled.
func (j *Journal) EscrowParked(escrowID string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow parked for settlement", logkey.Escrow, escrowID)
	})
}

// EscrowSettled renders the audit record of funds leaving an escrow.
func (j *Journal) EscrowSettled(escrowID, model, txHash, settler string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("escrow settled", logkey.Escrow, escrowID, logkey.Model, model, logkey.Tx, txHash, logkey.Settler, settler)
	})
}

// SettlementReconciled renders a settle found already on chain instead of broadcast again.
func (j *Journal) SettlementReconciled(escrowID, txHash string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("settle already on chain, reconciled", logkey.Escrow, escrowID, logkey.Tx, txHash)
	})
}

// SettledRecordDropped renders the irreversible removal of the row that named the escrow's settling key.
func (j *Journal) SettledRecordDropped(escrowID string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("settled escrow record dropped", logkey.Escrow, escrowID)
	})
}

// EscrowTickFailed renders the joined error of one escrow tick.
func (j *Journal) EscrowTickFailed(err error) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Error("escrow tick failed", logkey.Error, err)
	})
}

// TimeoutsSwept renders a sweep that found nonces owed a vote.
func (j *Journal) TimeoutsSwept(due, applied, failed int) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("execution timeouts swept",
			logkey.SweptDue, due, logkey.SweptApplied, applied, logkey.SweptFailed, failed)
	})
}

// SettleBroadcast renders a settle transaction the node accepted, before its commit is awaited.
func (j *Journal) SettleBroadcast(escrowID, txHash, settler string) {
	j.emitLine(KindEscrowTransition, func(lines logSink) {
		lines.Info("settle tx broadcast", logkey.Escrow, escrowID, logkey.Tx, txHash, logkey.Settler, settler)
	})
}

package journal

import "devshard/cmd/gateway/internal/logkey"

// EscrowPlanned renders what the funding planner decided for a model and the money it sized the decision from. See ../escrow/README.md, "The funding planner".
func (j *Journal) EscrowPlanned(model string, creates, retires int, reasons string, moneyShort bool, need, liquid uint64, fullCount int) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Info("escrow funding planned",
			logkey.Model, model, logkey.Creates, creates, logkey.Retires, retires, logkey.Reasons, reasons,
			logkey.MoneyShort, moneyShort, logkey.Need, need, logkey.Liquid, liquid, logkey.FullCount, fullCount)
	})
}

// EscrowStarved renders an escrow whose free money no longer pays a full-context race.
func (j *Journal) EscrowStarved(escrowID string, free uint64) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Info("escrow starved", logkey.Escrow, escrowID, logkey.Free, free)
	})
}

// EscrowFull renders a starved escrow whose free money pays a full-context race again.
func (j *Journal) EscrowFull(escrowID string, free uint64) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Info("escrow full again", logkey.Escrow, escrowID, logkey.Free, free)
	})
}

// FundingGuaranteeBroken renders the first tick of an episode in which a model holds fewer full escrows than its guarantee and cannot create them.
func (j *Journal) FundingGuaranteeBroken(model string, have, want int) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Warn("funding guarantee broken", logkey.Model, model, logkey.Have, have, logkey.Want, want)
	})
}

// FundingMisconfigured renders the first tick of an episode in which a model's amount cannot make a fresh escrow full.
func (j *Journal) FundingMisconfigured(model string, amount, need uint64) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Warn("model amount cannot fund a full escrow", logkey.Model, model, logkey.Amount, amount, logkey.Need, need)
	})
}

// EscrowBudgetReached renders the first tick of an episode in which a model wants more escrows than max_unsettled leaves room for.
func (j *Journal) EscrowBudgetReached(model string, counted, maxUnsettled int) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Warn("escrow budget reached", logkey.Model, model, logkey.Counted, counted, logkey.MaxUnsettled, maxUnsettled)
	})
}

// FundingPlanFailed renders a tick whose funding plan could not read the rows it plans from.
func (j *Journal) FundingPlanFailed(err error) {
	j.emitLine(KindFundingTransition, func(lines logSink) {
		lines.Warn("escrow funding plan skipped", logkey.Error, err)
	})
}

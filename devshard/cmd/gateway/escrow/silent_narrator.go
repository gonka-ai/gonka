package escrow

import (
	"time"

	"devshard/cmd/gateway/store"
)

type silentNarrator struct{}

func (silentNarrator) EscrowCreated(escrowID, model string, role store.Role, reason string, epoch uint64, txHash string) {
}
func (silentNarrator) EscrowRecovered(escrowID, model string, role store.Role, epoch uint64, txHash string) {
}
func (silentNarrator) EscrowCreateUnderfunded(model string, role store.Role, have, need uint64)   {}
func (silentNarrator) EscrowCreateBelowFloor(model string, role store.Role, amount, floor uint64) {}
func (silentNarrator) EscrowReserveTaken(escrowID string)                                         {}
func (silentNarrator) CommitmentCleared(txHash, model string, role store.Role, epoch uint64, reason string) {
}
func (silentNarrator) EscrowGoneFromChain(escrowID string)                                      {}
func (silentNarrator) EscrowDeadlineReached(escrowID string, chainEpoch uint64, settleBy int64) {}
func (silentNarrator) EscrowDeadlineUnsettled(escrowID string, settleBy int64, reason string)   {}
func (silentNarrator) SettleMarginShort(marginBlocks int64, blockTime, need time.Duration)      {}
func (silentNarrator) EscrowDeadlinesProjected(height, projected int64, staleFor time.Duration) {}
func (silentNarrator) EscrowChainFactsUnresolved(escrowID, reason string)                       {}
func (silentNarrator) EscrowMarkedForReplacement(escrowID, reason string)                       {}
func (silentNarrator) RotationSkipped(model string, role store.Role, epoch uint64)              {}
func (silentNarrator) RegularsPromotedToTemp(model string, epoch uint64, promoted int)          {}
func (silentNarrator) BridgePrepared(model string, epoch uint64, created, retired int)          {}
func (silentNarrator) BridgeFinished(model string, epoch uint64, created, retired int)          {}
func (silentNarrator) EscrowParked(escrowID string)                                             {}
func (silentNarrator) EscrowSettled(escrowID, model, txHash, settler string)                    {}
func (silentNarrator) SettlementReconciled(escrowID, txHash string)                             {}
func (silentNarrator) SettledRecordDropped(escrowID string)                                     {}
func (silentNarrator) EscrowTickFailed(err error)                                               {}
func (silentNarrator) TimeoutsSwept(due, applied, failed int)                                   {}
func (silentNarrator) EscrowPlanned(model string, creates, retires int, reasons string, moneyShort bool, need, liquid uint64, fullCount int) {
}
func (silentNarrator) EscrowStarved(escrowID string, free uint64)                  {}
func (silentNarrator) EscrowFull(escrowID string, free uint64)                     {}
func (silentNarrator) FundingGuaranteeBroken(model string, have, want int)         {}
func (silentNarrator) FundingMisconfigured(model string, amount, need uint64)      {}
func (silentNarrator) EscrowBudgetReached(model string, counted, maxUnsettled int) {}
func (silentNarrator) FundingPlanFailed(err error)                                 {}

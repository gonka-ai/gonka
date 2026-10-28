package escrow

import (
	"context"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
	"devshard/signing"
	"devshard/types"
)

// Satisfied by *chain.TxClient. See README.md, "What this package expects of others".
type escrowTxClient interface {
	CreateEscrow(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(txHash string) error) (chain.CreateEscrowResult, error)
	SettleEscrow(ctx context.Context, signer *signing.Secp256k1Signer, input chain.SettlementInput, onPrepared func(txHash string) error) (chain.SettleEscrowResult, error)
	TxCommitted(ctx context.Context, txHash string) (bool, error)
	GetTxEscrowID(ctx context.Context, txHash string) (escrowID uint64, found bool, err error)
	GetEscrow(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error)
}

// Satisfied by *chain.TxClient; it turns on everything that reads chain epochs. See README.md, "Chain epochs and amounts".
type escrowLookup interface {
	GetEscrow(ctx context.Context, escrowID string) (chain.EscrowInfo, bool, error)
}

// Satisfied by *store.Store.
type escrowStore interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
	UpsertDevshard(ctx context.Context, record store.DevshardRecord) error
	SetDevshardActive(ctx context.Context, escrowID string, active bool) error
	MarkDevshardGoneFromChain(ctx context.Context, escrowID string) error
	SetDevshardChainFacts(ctx context.Context, escrowID string, chainEpoch, amount uint64) error
	SetDevshardSettlementPending(ctx context.Context, escrowID string, pending bool) error
	ParkForSettlement(ctx context.Context, escrowID string) error
	ParkForSettlementIfActive(ctx context.Context, escrowID string) (bool, error)
	DevshardSettleTxHash(ctx context.Context, escrowID string) (string, time.Time, error)
	SetDevshardRotationRole(ctx context.Context, escrowID, role string) error
	SetDevshardSettleTxHash(ctx context.Context, escrowID, txHash string) error
	DeleteDevshard(ctx context.Context, escrowID string) error
	SaveCommitment(ctx context.Context, c store.Commitment) error
	LoadCommitments(ctx context.Context) ([]store.Commitment, error)
	DeleteCommitment(ctx context.Context, txHash string) error
	SaveRotationStatus(ctx context.Context, s store.RotationStatus) error
	WithRetry(ctx context.Context, fn func() error) error
}

// Satisfied by *chain.PhaseObserver.
type snapshotSource interface {
	Snapshot() chain.PhaseSnapshot
}

// SignerSource resolves the signer holding the key named by a PrivateKeyEnv.
type SignerSource interface {
	SignerFor(privateKeyEnv string) (*signing.Secp256k1Signer, error)
}

// TimeoutSweeper retries the execution timeouts no race is left to post, each tick and before a finalize; see README.md, "Settlement and retirement".
type TimeoutSweeper interface {
	SweepExecutionTimeouts(ctx context.Context, grace time.Duration, budget int) (due, applied, failed int)
	SweepBeforeFinalize(ctx context.Context, escrowID string, budget int) (due, applied, failed int, err error) // no race grace: finalize pays what stays open
}

// SweepRecorder counts what one sweep tick did.
type SweepRecorder interface {
	RecordSweep(due, applied, failed int)
}

type SettlementSource interface {
	Retire(escrowID string) error // synchronous: no nonce can be committed on the escrow after it returns
	IsBusy(escrowID string) bool
	Finalize(ctx context.Context, escrowID string) error // idempotent: no-op if already finalized
	BuildSettlement(ctx context.Context, escrowID string) (chain.SettlementInput, error)
}

// nonceCapRetirer is a SettlementSource whose Retire for a nonce cap still lets the escrow's running races escalate on it while it stays busy. See ../docs/routing.md, "Picking an escrow".
type nonceCapRetirer interface {
	RetireAtNonceCap(escrowID string) error
}

// nonceCapRetirement marks the ctx of a retirement for the escrow's nonce cap, the one reason routing exempts.
type nonceCapRetirement struct{}

func retiringAtNonceCap(ctx context.Context) context.Context {
	return context.WithValue(ctx, nonceCapRetirement{}, true)
}

func atNonceCap(ctx context.Context) bool {
	capped, _ := ctx.Value(nonceCapRetirement{}).(bool)
	return capped
}

// ExhaustionProbe names the reason routing would retire an escrow on, empty while it may still be picked. See README.md, "Depletion marks".
type ExhaustionProbe interface {
	Exhaustion(escrowID string) scheduler.ExhaustionReason
}

// EscrowMoney is one live escrow's money as the funding planner reads it. See README.md, "The funding planner".
type EscrowMoney struct {
	Config      types.SessionConfig
	Balance     uint64
	TokenPrice  uint64
	FeePerNonce uint64
	Open        []types.InferenceRecord
	Holds       uint64
}

// FundingReader reads a live escrow's money for the funding planner; the composition root satisfies it.
type FundingReader interface {
	EscrowMoney(escrowID string) (EscrowMoney, bool)
}

// lifecycleNarrator is satisfied by *journal.Journal; each method names one transition an operator reads the log for. See README.md, "What this package expects of others".
type lifecycleNarrator interface {
	EscrowCreated(escrowID, model, role, reason string, epoch uint64, txHash string)
	EscrowRecovered(escrowID, model, role string, epoch uint64, txHash string)
	EscrowCreateUnderfunded(model, role string, have, need uint64)
	EscrowCreateBelowFloor(model, role string, amount, floor uint64)
	EscrowReserveTaken(escrowID string)
	CommitmentCleared(txHash, model, role string, epoch uint64, reason string)
	EscrowGoneFromChain(escrowID string)
	EscrowDeadlineReached(escrowID string, chainEpoch uint64, settleBy int64)
	EscrowDeadlineUnsettled(escrowID string, settleBy int64, reason string)
	SettleMarginShort(marginBlocks int64, blockTime, need time.Duration)
	EscrowDeadlinesProjected(height, projected int64, staleFor time.Duration)
	EscrowChainFactsUnresolved(escrowID, reason string)
	EscrowMarkedForReplacement(escrowID, reason string)
	RotationSkipped(model, role string, epoch uint64)
	RegularsPromotedToTemp(model string, epoch uint64, promoted int)
	BridgePrepared(model string, epoch uint64, created, retired int)
	BridgeFinished(model string, epoch uint64, created, retired int)
	EscrowParked(escrowID string)
	EscrowSettled(escrowID, model, txHash, settler string)
	SettlementReconciled(escrowID, txHash string)
	SettledRecordDropped(escrowID string)
	EscrowTickFailed(err error)
	TimeoutsSwept(due, applied, failed int)
	EscrowPlanned(model string, creates, retires int, reasons string, moneyShort bool, need, liquid uint64, fullCount int)
	EscrowStarved(escrowID string, free uint64)
	EscrowFull(escrowID string, free uint64)
	FundingGuaranteeBroken(model string, have, want int)
	FundingMisconfigured(model string, amount, need uint64)
	EscrowBudgetReached(model string, counted, maxUnsettled int)
	FundingPlanFailed(err error)
}

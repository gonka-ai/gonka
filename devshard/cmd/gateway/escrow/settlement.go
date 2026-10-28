package escrow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
)

// ErrUnknownEscrow means the registry has no row for the escrow, ErrEscrowPruned that the chain pruned it after its settlement window, ErrEscrowSettledOnChain that the chain already holds its settlement. See README.md, "Settlement and retirement".
var (
	ErrDevshardBusy = errors.New("devshard busy")

	ErrUnknownEscrow = errors.New("unknown escrow")

	ErrSettlementInFlight = errors.New("settlement already in flight")

	ErrEscrowPruned = errors.New("escrow pruned on chain")

	ErrEscrowSettledOnChain = errors.New("escrow already settled on chain")
)

type parkNarration bool

// See escrows.md, "Settlement and retirement".
const (
	pendingSettleBudget    = 4
	urgentSettleBudget     = 8
	failedSweepRetryWindow = 4 * TickInterval

	narratePark       parkNarration = true
	skipParkNarration parkNarration = false
)

// parkedToSettle is the settlement order: the nearest deadline first, then the escrow id.
func (m *Manager) parkedToSettle(snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) []pendingSettle {
	policy := newSettlementPolicy(m.config.Load().Rotation)
	var parked []pendingSettle
	for _, record := range devshards {
		if record.Active || !record.SettlementPending || goneFromChain(record) {
			continue
		}
		if !policy.settles(record.Model, ownEpochOver(record, snapshot)) {
			continue
		}
		parked = append(parked, pendingSettle{record: record, deadline: m.deadlineOf(record, snapshot)})
	}
	slices.SortStableFunc(parked, func(left, right pendingSettle) int {
		return cmp.Or(cmp.Compare(left.deadline.order(), right.deadline.order()), cmp.Compare(left.record.EscrowID, right.record.EscrowID))
	})
	return parked
}

func (m *Manager) settleParked(ctx context.Context, parked []pendingSettle) error {
	admitted := m.admitParked(parked)
	var (
		settling  sync.WaitGroup
		collected sync.Mutex
		failures  []error
	)
	for _, settle := range admitted {
		settling.Go(func() {
			defer settle.leave()
			_, err := m.settleAndDrop(ctx, settle.record, settle.urgent)
			if err == nil || nothingLeftToSettle(err) || deferredRetire(err) || errors.Is(err, store.ErrDevshardNotFound) {
				return
			}
			collected.Lock()
			failures = append(failures, err)
			collected.Unlock()
		})
	}
	settling.Wait()
	return errors.Join(failures...)
}

func (m *Manager) admitParked(parked []pendingSettle) []admittedSettle {
	var admitted []admittedSettle
	for _, pending := range parked {
		if m.settlements.has(pending.record.EscrowID) {
			continue
		}
		urgent := pending.deadline.inMargin
		slots, limit := &m.budgetedSettles, pendingSettleBudget
		if urgent {
			slots, limit = &m.urgentSettles, urgentSettleBudget
		}
		if leave, entered := slots.enterWithin(pending.record.EscrowID, limit); entered {
			admitted = append(admitted, admittedSettle{record: pending.record, urgent: urgent, leave: leave})
		}
	}
	return admitted
}

type admittedSettle struct {
	record store.DevshardRecord
	urgent bool
	leave  func()
}

type pendingSettle struct {
	record   store.DevshardRecord
	deadline settleDeadline
}

// Settle settles one escrow on demand, on the same path the rotation lifecycle uses; force crosses the busy check alone. See ../docs/operations.md.
func (m *Manager) Settle(ctx context.Context, escrowID string, force bool) (chain.SettleEscrowResult, error) {
	devshards, err := m.store.ListDevshards(ctx)
	if err != nil {
		return chain.SettleEscrowResult{}, fmt.Errorf("listing devshards: %w", err)
	}
	for _, record := range devshards {
		if record.EscrowID != escrowID {
			continue
		}
		return m.settleAndDrop(ctx, record, force)
	}
	return chain.SettleEscrowResult{}, fmt.Errorf("settling escrow %s: %w", escrowID, ErrUnknownEscrow)
}

func (m *Manager) park(ctx context.Context, escrowID string) error {
	return m.parkAs(ctx, escrowID, narratePark)
}

// parkAs writes inactive and pending in one statement, then stops routing -- the order the busy check relies on.
func (m *Manager) parkAs(ctx context.Context, escrowID string, narration parkNarration) error {
	if err := m.store.WithRetry(ctx, func() error {
		return m.store.ParkForSettlement(ctx, escrowID)
	}); err != nil {
		return fmt.Errorf("parking escrow %s for settlement: %w", escrowID, err)
	}
	return m.stopRouting(ctx, escrowID, narration)
}

// parkIfServing parks only a serving escrow and reports whether this call parked it. See README.md, "Depletion marks".
func (m *Manager) parkIfServing(ctx context.Context, escrowID string) (bool, error) {
	var parked bool
	if err := m.store.WithRetry(ctx, func() error {
		var err error
		parked, err = m.store.ParkForSettlementIfActive(ctx, escrowID)
		return err
	}); err != nil {
		return false, fmt.Errorf("parking escrow %s for settlement: %w", escrowID, err)
	}
	if !parked {
		return false, nil
	}
	return true, m.stopRouting(ctx, escrowID, narratePark)
}

func (m *Manager) stopRouting(ctx context.Context, escrowID string, narration parkNarration) error {
	retire := m.settlementSource.Retire
	if capped, ok := m.settlementSource.(nonceCapRetirer); ok && atNonceCap(ctx) {
		retire = capped.RetireAtNonceCap
	}
	if err := retire(escrowID); err != nil {
		return fmt.Errorf("retiring escrow %s from routing: %w", escrowID, err)
	}
	if narration == narratePark && m.narrator != nil {
		m.narrator.EscrowParked(escrowID)
	}
	return nil
}

func parkNarrationOf(record store.DevshardRecord) parkNarration {
	if !record.Active && record.SettlementPending {
		return skipParkNarration
	}
	return narratePark
}

// settle clears SettlementPending only once the broadcast is confirmed. See README.md, "Settlement and retirement".
func (m *Manager) settle(ctx context.Context, record store.DevshardRecord, force bool) (chain.SettleEscrowResult, error) {
	leave, busy := m.settlements.enter(record.EscrowID)
	if busy {
		return chain.SettleEscrowResult{}, ErrSettlementInFlight
	}
	defer leave()
	return m.settleEntered(ctx, record, force)
}

func (m *Manager) settleEntered(ctx context.Context, record store.DevshardRecord, force bool) (chain.SettleEscrowResult, error) {
	// Parked before the reconciliation: the caller deletes the row on success, and a row that is gone can no longer un-publish the escrow.
	if err := m.parkAs(ctx, record.EscrowID, parkNarrationOf(record)); err != nil {
		return chain.SettleEscrowResult{}, err
	}

	if hash, settled, err := m.alreadySettled(ctx, record); err != nil {
		return chain.SettleEscrowResult{}, err
	} else if settled {
		return chain.SettleEscrowResult{EscrowID: numericEscrowID(record.EscrowID), TxHash: hash}, nil
	}

	// busy is a deferred-settle signal, not a failure: the now-retired escrow drains, then a retrigger settles it.
	if !force && m.settlementSource.IsBusy(record.EscrowID) {
		return chain.SettleEscrowResult{}, ErrDevshardBusy
	}

	if ended := m.endedOnChain(ctx, record); ended != nil {
		return chain.SettleEscrowResult{}, fmt.Errorf("settling escrow %s: %w", record.EscrowID, ended)
	}

	signer, err := m.signer.SignerFor(record.PrivateKeyEnv)
	if err != nil {
		return chain.SettleEscrowResult{}, fmt.Errorf("resolving signer for escrow %s: %w", record.EscrowID, err)
	}
	if err := m.sweepBeforeFinalize(ctx, record.EscrowID, force); err != nil {
		return chain.SettleEscrowResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return chain.SettleEscrowResult{}, fmt.Errorf("settling escrow %s: %w", record.EscrowID, err)
	}
	if err := m.settlementSource.Finalize(ctx, record.EscrowID); err != nil {
		return chain.SettleEscrowResult{}, fmt.Errorf("finalizing escrow %s: %w", record.EscrowID, err)
	}
	input, err := m.settlementSource.BuildSettlement(ctx, record.EscrowID)
	if err != nil {
		return chain.SettleEscrowResult{}, fmt.Errorf("building settlement for escrow %s: %w", record.EscrowID, err)
	}
	recordHash := func(txHash string) error {
		return m.store.WithRetry(ctx, func() error { return m.store.SetDevshardSettleTxHash(ctx, record.EscrowID, txHash) })
	}
	result, err := m.tx.SettleEscrow(ctx, signer, input, recordHash)
	if err != nil {
		failure := fmt.Errorf("settling escrow %s: %w", record.EscrowID, err)
		if errors.Is(err, chain.ErrTxRejected) {
			return chain.SettleEscrowResult{}, errors.Join(failure, m.clearSettleTxHash(ctx, record.EscrowID))
		}
		return chain.SettleEscrowResult{}, failure
	}
	if m.narrator != nil {
		m.narrator.EscrowSettled(record.EscrowID, record.Model, result.TxHash, result.Settler)
	}

	if err := m.store.WithRetry(ctx, func() error {
		return m.store.SetDevshardSettlementPending(ctx, record.EscrowID, false)
	}); err != nil {
		return result, fmt.Errorf("clearing settlement pending for escrow %s: %w", record.EscrowID, err)
	}
	return result, nil
}

func (m *Manager) sweepBeforeFinalize(ctx context.Context, escrowID string, force bool) error {
	if m.timeoutSweeper == nil {
		return nil
	}
	budget := int(m.config.Load().TimeoutSweep.BudgetPerTick)
	if budget <= 0 {
		return nil
	}
	due, applied, failed, err := m.timeoutSweeper.SweepBeforeFinalize(ctx, escrowID, budget)
	m.recordSweep(due, applied, failed)
	if err != nil {
		return fmt.Errorf("sweeping execution timeouts of escrow %s: %w", escrowID, err)
	}
	if !force && due >= budget && applied > 0 {
		return fmt.Errorf("%w: escrow %s spent its timeout sweep budget with votes still landing", ErrDevshardBusy, escrowID)
	}
	if !force && failed > 0 && ctx.Err() == nil && m.sweepRetries.retrying(escrowID, m.now()) {
		return fmt.Errorf("%w: escrow %s failed %d timeout votes; finalize waits to retry them", ErrDevshardBusy, escrowID, failed)
	}
	m.sweepRetries.forget(escrowID)
	return nil
}

// endedOnChain fails open: only the chain answering that the escrow is already settled, or absent once its epoch can have been pruned, counts. See escrows.md, "An escrow the chain has pruned".
func (m *Manager) endedOnChain(ctx context.Context, record store.DevshardRecord) error {
	onChain, found, err := m.tx.GetEscrow(ctx, record.EscrowID)
	answered := err == nil
	switch {
	case answered && !found && m.prunableOnChain(record):
		return ErrEscrowPruned
	case answered && onChain.Settled:
		return ErrEscrowSettledOnChain
	}
	return nil
}

func nothingLeftToSettle(err error) bool {
	return errors.Is(err, ErrEscrowPruned) || errors.Is(err, ErrEscrowSettledOnChain)
}

// settleAndDrop drops the row once nothing is left to settle: after a confirmed settlement, or once the chain has pruned or already settled the escrow.
func (m *Manager) settleAndDrop(ctx context.Context, record store.DevshardRecord, force bool) (chain.SettleEscrowResult, error) {
	leave, busy := m.settlements.enter(record.EscrowID)
	if busy {
		return chain.SettleEscrowResult{}, ErrSettlementInFlight
	}
	defer leave()
	result, err := m.settleEntered(ctx, record, force)
	if err != nil && !nothingLeftToSettle(err) {
		return result, err
	}
	if dropErr := m.deleteSettled(ctx, record.EscrowID); dropErr != nil {
		return result, dropErr
	}
	return result, err
}

// alreadySettled reports whether the transaction this escrow last broadcast landed. See escrows.md, "Settlement and retirement".
func (m *Manager) alreadySettled(ctx context.Context, record store.DevshardRecord) (string, bool, error) {
	hash, broadcastAt, err := m.store.DevshardSettleTxHash(ctx, record.EscrowID)
	if err != nil {
		return "", false, fmt.Errorf("reading settle tx of escrow %s: %w", record.EscrowID, err)
	}
	if hash == "" {
		return "", false, nil
	}
	succeeded, err := m.tx.TxCommitted(ctx, hash)
	switch {
	case errors.Is(err, chain.ErrTxNotFound) && m.settleTxMayStillLand(broadcastAt):
		// Not indexed yet is not never landed: an unordered tx stays landable for its whole TTL.
		return "", false, ErrSettlementInFlight
	case errors.Is(err, chain.ErrTxNotFound):
		return "", false, m.clearSettleTxHash(ctx, record.EscrowID) // past its TTL: a fresh transaction is right
	case err != nil:
		return "", false, fmt.Errorf("checking settle tx %s for escrow %s: %w", hash, record.EscrowID, err)
	case !succeeded:
		return "", false, m.clearSettleTxHash(ctx, record.EscrowID) // committed and rejected: retry is right
	}
	if m.narrator != nil {
		m.narrator.SettlementReconciled(record.EscrowID, hash)
	}
	return hash, true, nil
}

// A missing broadcast stamp counts as past the window -- the create path defaults the other way. See README.md, "Reconciling a settle that may already have landed".
func (m *Manager) settleTxMayStillLand(broadcastAt time.Time) bool {
	if broadcastAt.IsZero() {
		return false
	}
	return m.now().Sub(broadcastAt) <= commitmentReconcileGrace
}

func (m *Manager) clearSettleTxHash(ctx context.Context, escrowID string) error {
	if err := m.store.WithRetry(ctx, func() error { return m.store.SetDevshardSettleTxHash(ctx, escrowID, "") }); err != nil {
		return fmt.Errorf("clearing settle tx hash for escrow %s: %w", escrowID, err)
	}
	return nil
}

func numericEscrowID(escrowID string) uint64 {
	parsed, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

// retire honors the model's settlement policy. See README.md, "Settlement and retirement".
func (m *Manager) retire(ctx context.Context, record store.DevshardRecord) error {
	// Parked only: the row names the sole key that can settle this escrow later, so it outlives retirement.
	if !newSettlementPolicy(m.config.Load().Rotation).settlesAtRetirement(record.Model) {
		return m.park(ctx, record.EscrowID)
	}

	if _, err := m.settleAndDrop(ctx, record, false); err != nil && !nothingLeftToSettle(err) {
		return err // busy, deduped or broadcast failure: stays registered, inactive, pending -- not deleted.
	}
	return nil
}

// deleteSettled drops the row that named the only key able to settle the escrow, so it runs only after success.
func (m *Manager) deleteSettled(ctx context.Context, escrowID string) error {
	if err := m.store.WithRetry(ctx, func() error { return m.store.DeleteDevshard(ctx, escrowID) }); err != nil {
		return fmt.Errorf("deleting settled escrow %s: %w", escrowID, err)
	}
	m.sweepRetries.forget(escrowID)
	if m.narrator != nil {
		m.narrator.SettledRecordDropped(escrowID)
	}
	return nil
}

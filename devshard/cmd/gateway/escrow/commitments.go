package escrow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

// commitmentIndexLagMargin allows for a landed tx staying unqueryable past the chain's unordered-tx TTL.
const (
	commitmentIndexLagMargin = 2 * time.Minute
	commitmentReconcileGrace = chain.UnorderedTxTTL + commitmentIndexLagMargin
)

// ErrAmountBelowFloor marks a create refused before broadcast because its amount cannot pay one full-context request of its model. See README.md, "Creating an escrow".
var ErrAmountBelowFloor = errors.New("escrow amount cannot pay one full-context request of its model")

type Manager struct {
	tx        escrowTxClient
	store     escrowStore
	snapshots snapshotSource
	signer    SignerSource
	breaker   *createBreaker
	now       func() time.Time

	config           *config.Holder
	routePrefix      string
	settlementSource SettlementSource
	exhaustion       ExhaustionProbe
	funds            FundingReader
	chainFacts       escrowLookup
	chainFactsAfter  string
	prunedCheckAfter string
	settlements      inFlightSet
	budgetedSettles  inFlightSet
	urgentSettles    inFlightSet
	checks           inFlightSet

	depleted            depletionMarks
	missing             markSet
	underfundedNarrated markSet
	belowFloorNarrated  markSet
	unresolvedNarrated  markSet
	reserveTaken        markSet
	moneyShort          markSet
	deadlineNarrated    map[string]bool
	deadlineCounts      deadlineCounter
	pace                blockPace
	stale               staleProjection
	wakeup              chan struct{}

	timeoutSweeper TimeoutSweeper
	sweepRecorder  SweepRecorder
	sweepRetries   retryWindows
	narrator       lifecycleNarrator
	sweeping       atomic.Bool
	offTickWork    sync.WaitGroup
	planner        fundingPlanner

	lifecycleMu sync.Mutex
	done        chan struct{}
	cancel      context.CancelFunc
}

// CreateEscrow creates one escrow on demand, on the same durable-intent path rotation uses. See README.md, "Creating an escrow".
func (m *Manager) CreateEscrow(ctx context.Context, model ModelConfig) (chain.CreateEscrowResult, error) {
	snapshot := m.snapshots.Snapshot()
	return m.createFor(ctx, model, roleRegular, snapshot.EpochIndex, string(createdByOperator), snapshot)
}

// A failed intent-commitment write (in onPrepared) aborts before any chain broadcast: no broadcast without durable intent.
func (m *Manager) createFor(ctx context.Context, model ModelConfig, role string, label uint64, reason string, snapshot chain.PhaseSnapshot) (chain.CreateEscrowResult, error) {
	if floor, priced := m.creationFloor(model.ModelID, snapshot); priced && model.Amount < floor {
		m.narrateBelowFloor(model, role, floor)
		return chain.CreateEscrowResult{}, fmt.Errorf("creating escrow for %s/%s: %w: amount %d, floor %d", model.ModelID, role, ErrAmountBelowFloor, model.Amount, floor)
	}
	signer, err := m.signer.SignerFor(model.PrivateKeyEnv)
	if err != nil {
		return chain.CreateEscrowResult{}, fmt.Errorf("resolving signer for %s: %w", model.PrivateKeyEnv, err)
	}

	c := store.Commitment{
		Model:         model.ModelID,
		Role:          role,
		Epoch:         label,
		PrivateKeyEnv: model.PrivateKeyEnv,
		BlockHeight:   snapshot.BlockHeight,
	}
	onPrepared := func(txHash string) error {
		c.TxHash = txHash
		c.CreatedAt = m.now()
		return m.store.WithRetry(ctx, func() error { return m.store.SaveCommitment(ctx, c) })
	}

	result, err := m.tx.CreateEscrow(ctx, signer, model.Amount, model.ModelID, onPrepared)
	if err != nil {
		m.narrateUnderfunded(model.ModelID, role, err)
		failure := fmt.Errorf("creating escrow for %s/%s: %w", model.ModelID, role, err)
		if errors.Is(err, chain.ErrTxRejected) {
			return chain.CreateEscrowResult{}, errors.Join(failure, m.clearCommitment(ctx, c, commitmentClearedRejected))
		}
		return chain.CreateEscrowResult{}, failure
	}
	escrowID := strconv.FormatUint(result.EscrowID, 10)
	if m.narrator != nil {
		m.narrator.EscrowCreated(escrowID, model.ModelID, role, reason, label, result.TxHash)
	}
	return result, m.persistEscrow(ctx, escrowID, c)
}

// persistEscrow registers the escrow a commitment created, then drops the commitment. See escrows.md, "Creating an escrow".
func (m *Manager) persistEscrow(ctx context.Context, escrowID string, c store.Commitment) error {
	registered, err := m.escrowRegistered(ctx, escrowID)
	if err != nil {
		return err
	}
	if !registered {
		record := store.DevshardRecord{
			EscrowID:      escrowID,
			PrivateKeyEnv: c.PrivateKeyEnv,
			Model:         c.Model,
			Active:        true,
			RotationRole:  c.Role,
			RotationEpoch: int64(c.Epoch),
			RoutePrefix:   m.routePrefix,
		}
		if err := m.store.WithRetry(ctx, func() error { return m.store.UpsertDevshard(ctx, record) }); err != nil {
			return fmt.Errorf("registering escrow %s: %w", escrowID, err)
		}
		m.readChainFacts(ctx, escrowID)
	}
	if err := m.clearCommitmentRow(ctx, escrowID, c.TxHash); err != nil {
		return err
	}
	// Both paths: an escrow found already registered is a create that succeeded.
	m.breaker.reset(c.Model, c.Role)
	m.underfundedNarrated.forget(createBreakerKey(c.Model, c.Role))
	m.belowFloorNarrated.forget(createBreakerKey(c.Model, c.Role))
	return nil
}

func (m *Manager) creationFloor(modelID string, snapshot chain.PhaseSnapshot) (uint64, bool) {
	if snapshot.TokenPrice == 0 {
		return 0, false
	}
	reserve := m.config.Load().Limits.RetirementReserve(modelID, snapshot.Models[modelID].MaxModelLen)
	cost, priced := scheduler.RequestCost(reserve, snapshot.TokenPrice, snapshot.FeePerNonce)
	floor := cost + snapshot.CreateDevshardFee
	if !priced || floor < cost {
		return math.MaxUint64, true
	}
	return floor, true
}

func (m *Manager) narrateBelowFloor(model ModelConfig, role string, floor uint64) {
	if !m.belowFloorNarrated.mark(createBreakerKey(model.ModelID, role)) || m.narrator == nil {
		return
	}
	m.narrator.EscrowCreateBelowFloor(model.ModelID, role, model.Amount, floor)
}

// narrateUnderfunded names a wallet that cannot pay once per (model, role), until a create of it succeeds.
func (m *Manager) narrateUnderfunded(modelID, role string, err error) {
	var underfunded *chain.WalletUnderfundedError
	if !errors.As(err, &underfunded) || !m.underfundedNarrated.mark(createBreakerKey(modelID, role)) || m.narrator == nil {
		return
	}
	m.narrator.EscrowCreateUnderfunded(modelID, role, underfunded.Have, underfunded.Need)
}

func (m *Manager) escrowRegistered(ctx context.Context, escrowID string) (bool, error) {
	records, err := m.store.ListDevshards(ctx)
	if err != nil {
		return false, fmt.Errorf("listing devshards for escrow %s: %w", escrowID, err)
	}
	return slices.ContainsFunc(records, func(record store.DevshardRecord) bool {
		return record.EscrowID == escrowID
	}), nil
}

func (m *Manager) clearCommitmentRow(ctx context.Context, escrowID, txHash string) error {
	if err := m.store.WithRetry(ctx, func() error { return m.store.DeleteCommitment(ctx, txHash) }); err != nil {
		return fmt.Errorf("clearing commitment for escrow %s: %w", escrowID, err)
	}
	return nil
}

func (m *Manager) reconcile(ctx context.Context) error {
	commitments, err := m.store.LoadCommitments(ctx)
	if err != nil {
		return fmt.Errorf("loading commitments: %w", err)
	}

	var errs []error
	for _, c := range commitments {
		if err := m.reconcileOne(ctx, c); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) reconcileOne(ctx context.Context, c store.Commitment) error {
	createdEscrowID, found, err := m.tx.GetTxEscrowID(ctx, c.TxHash)
	switch {
	case err == nil && found:
		escrowID := strconv.FormatUint(createdEscrowID, 10)
		if persistErr := m.persistEscrow(ctx, escrowID, c); persistErr != nil {
			return persistErr
		}
		if m.narrator != nil {
			m.narrator.EscrowRecovered(escrowID, c.Model, c.Role, c.Epoch, c.TxHash)
		}
		return nil
	case err == nil && !found:
		return m.clearCommitment(ctx, c, commitmentClearedNoEscrow) // committed but produced no escrow event: terminal
	case errors.Is(err, chain.ErrTxNotFound):
		if m.txMayStillLand(c) {
			return nil // unordered tx may still land: keep, retry next tick
		}
		return m.clearCommitment(ctx, c, commitmentClearedCannotLand) // past its TTL
	default:
		return fmt.Errorf("querying tx %s: %w", c.TxHash, err) // endpoint unreachable: keep, retry next tick
	}
}

// clearCommitment takes the reason rather than deriving it: the two callers know it, the row does not.
func (m *Manager) clearCommitment(ctx context.Context, c store.Commitment, reason string) error {
	if err := m.store.WithRetry(ctx, func() error { return m.store.DeleteCommitment(ctx, c.TxHash) }); err != nil {
		return fmt.Errorf("clearing commitment %s: %w", c.TxHash, err)
	}
	if m.narrator != nil {
		m.narrator.CommitmentCleared(c.TxHash, c.Model, c.Role, c.Epoch, reason)
	}
	return nil
}

// A zero CreatedAt (malformed/legacy row) defensively counts as still-pending.
func (m *Manager) txMayStillLand(c store.Commitment) bool {
	if c.CreatedAt.IsZero() {
		return true
	}
	return m.now().Sub(c.CreatedAt) <= commitmentReconcileGrace
}

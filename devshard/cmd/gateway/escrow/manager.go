package escrow

import (
	"context"
	"errors"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
)

// TickInterval is how often the manager plans, settles and republishes. See README.md, "The tick".
const TickInterval = 15 * time.Second

type Deps struct {
	Tx          escrowTxClient
	Store       escrowStore
	Snapshots   snapshotSource
	Settlement  SettlementSource
	Exhaustion  ExhaustionProbe
	Funds       FundingReader
	ChainFacts  escrowLookup
	Timeouts    TimeoutSweeper
	Sweeps      SweepRecorder
	Narrator    lifecycleNarrator
	Signer      SignerSource
	Config      *config.Holder
	Now         func() time.Time
	RoutePrefix string
}

// NewManager refuses a dependency set it cannot run a tick with, rather than panicking on the first one.
func NewManager(d Deps) (*Manager, error) {
	switch {
	case d.Config == nil:
		return nil, errors.New("escrow: Config is required")
	case d.Tx == nil:
		return nil, errors.New("escrow: Tx is required")
	case d.Store == nil:
		return nil, errors.New("escrow: Store is required")
	case d.Snapshots == nil:
		return nil, errors.New("escrow: Snapshots is required")
	case d.Signer == nil:
		return nil, errors.New("escrow: Signer is required")
	case d.Now == nil:
		return nil, errors.New("escrow: Now is required")
	}
	return &Manager{
		tx:               d.Tx,
		store:            d.Store,
		snapshots:        d.Snapshots,
		signer:           d.Signer,
		breaker:          newCreateBreaker(),
		now:              d.Now,
		config:           d.Config,
		settlementSource: d.Settlement,
		exhaustion:       d.Exhaustion,
		funds:            d.Funds,
		chainFacts:       d.ChainFacts,
		timeoutSweeper:   d.Timeouts,
		sweepRecorder:    d.Sweeps,
		narrator:         d.Narrator,
		routePrefix:      d.RoutePrefix,
		wakeup:           make(chan struct{}, 1),
		planner:          fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}},
	}, nil
}

// Start is idempotent: a call while already running is a no-op.
func (m *Manager) Start(ctx context.Context) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.done, m.cancel = done, cancel

	go func() {
		defer cancel()
		defer close(done)
		m.runTick(ctx, true)
		ticker := time.NewTicker(TickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runTick(ctx, true)
			case <-m.wakeup:
				m.runTick(ctx, false)
			}
		}
	}()
}

func (m *Manager) runTick(ctx context.Context, scheduled bool) {
	if err := m.tickWith(ctx, scheduled); err != nil && m.narrator != nil {
		m.narrator.EscrowTickFailed(err)
	}
}

// Stop is idempotent and a barrier for every caller. See escrows.md, "The tick".
func (m *Manager) Stop() {
	m.lifecycleMu.Lock()
	done, cancel := m.done, m.cancel
	m.cancel = nil
	m.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	m.offTickWork.Wait()
}

func (m *Manager) settleOffTick(ctx context.Context, snapshot chain.PhaseSnapshot, devshards []store.DevshardRecord) {
	parked := m.parkedToSettle(snapshot, devshards)
	if len(parked) == 0 {
		return
	}
	m.offTickWork.Go(func() {
		if err := m.settleParked(ctx, parked); err != nil && m.narrator != nil {
			m.narrator.EscrowTickFailed(err)
		}
	})
}

// sweepTimeouts runs off the tick rather than on it: one vote round can outlast the tick interval, and
// a second sweep over the same escrows would double the load the budget exists to bound.
func (m *Manager) sweepTimeouts(ctx context.Context) {
	settings := m.config.Load().TimeoutSweep
	budget := int(settings.BudgetPerTick)
	if m.timeoutSweeper == nil || budget <= 0 {
		return
	}
	if !m.sweeping.CompareAndSwap(false, true) {
		return
	}
	m.offTickWork.Go(func() {
		defer m.sweeping.Store(false)
		grace := time.Duration(settings.GraceSeconds) * time.Second
		m.recordSweep(m.timeoutSweeper.SweepExecutionTimeouts(ctx, grace, budget))
	})
}

func (m *Manager) recordSweep(due, applied, failed int) {
	if m.sweepRecorder != nil {
		m.sweepRecorder.RecordSweep(due, applied, failed)
	}
	if due == 0 || m.narrator == nil {
		return
	}
	m.narrator.TimeoutsSwept(due, applied, failed)
}

// tick is one whole tick, the work it starts off the tick included, so a caller reads what the tick did.
func (m *Manager) tick(ctx context.Context) error {
	err := m.tickWith(ctx, true)
	m.offTickWork.Wait()
	return err
}

func (m *Manager) tickWith(ctx context.Context, scheduled bool) error {
	lifecycleErr := m.runLifecycle(ctx)
	return errors.Join(lifecycleErr, m.planFunding(ctx, scheduled))
}

func (m *Manager) runLifecycle(ctx context.Context) error {
	reconcileErr := m.reconcile(ctx) // crash recovery must not depend on the rotation toggle

	devshards, err := m.store.ListDevshards(ctx)
	if err != nil {
		return errors.Join(reconcileErr, err)
	}
	devshards = m.resolveChainFacts(ctx, devshards)
	// Pulled, not subscribed: a 15s poll is equivalent at this cadence and avoids callback races.
	snapshot := m.snapshots.Snapshot()
	m.checkSettleMargin(snapshot)
	deadlineSnapshot := m.projectStale(snapshot)
	devshards, deadlineErr := m.parkAtDeadline(ctx, deadlineSnapshot, devshards)
	// Parked escrows must settle whatever the rotation toggle says: nothing else will ever pick them up.
	m.settleOffTick(ctx, deadlineSnapshot, devshards)
	devshards, prunedErr := m.markPrunedPastDeadline(ctx, snapshot, devshards)
	// An escrow gone from chain must stop taking traffic whatever the rotation toggle says.
	missingErr := m.checkMissing(ctx)
	m.sweepTimeouts(ctx)

	configuration := m.config.Load()
	models, modelsErr := rotationModels(configuration.Rotation)
	plannedErr := m.runPlannedLifecycle(ctx, snapshot, models, devshards, configuration.Rotation)
	return errors.Join(reconcileErr, deadlineErr, prunedErr, missingErr, modelsErr, plannedErr)
}

// rotationModels is empty when rotation is off, so no caller downstream has to re-read the toggle.
func rotationModels(rotation config.Rotation) ([]ModelConfig, error) {
	if !rotation.Enabled {
		return nil, nil
	}
	return parseModels(rotation.ModelsJSON)
}

// snapshotHasNoEpochYet: an escrow created under it belongs to no epoch. See escrows.md, "The funding planner".
func snapshotHasNoEpochYet(snapshot chain.PhaseSnapshot) bool {
	return snapshot.EpochIndex == 0 || snapshot.BlockHeight == 0
}

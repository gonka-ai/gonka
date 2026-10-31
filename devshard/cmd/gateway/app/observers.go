package app

import (
	"sync"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/scheduler"
)

// tracedDispatches counts dispatch events and hands the ones worth a line or a ledger fact to the journal. See ../README.md, "Two readers of one fact".
type tracedDispatches struct {
	recorder *metrics.DispatchRecorder
	steps    *metrics.StepRecorder
	events   *journal.Journal
	ledger   *nonces.Recorder
}

// GhostBurned is counted by escrow and reason, never by nonce: a counter keyed by nonce would grow without end.
func (t tracedDispatches) GhostBurned(escrowID string, burned scheduler.Burn) {
	t.recorder.GhostBurned(escrowID, burned.Participant, burned.Reason)
	t.events.GhostBurned(escrowID, burned)
}

// BurnBudgetExhausted is rare and changes what the escrow does: queued callers now wait rather than spend.
func (t tracedDispatches) BurnBudgetExhausted(escrowID string) {
	t.recorder.BurnBudgetExhausted(escrowID)
	t.events.BurnBudgetExhausted(escrowID)
}

func (t tracedDispatches) NonceAssigned(escrowID string, nonce uint64, profile scheduler.RequestProfile) {
	t.ledger.NonceAssigned(escrowID, nonce, profile)
}

func (t tracedDispatches) NonceQueued(_ string, waited time.Duration) {
	if t.steps != nil {
		t.steps.QueueWaited(waited)
	}
}

// ExcludedHostServed has no counter: the line is the only record that a request's exclusion lapsed.
func (t tracedDispatches) ExcludedHostServed(escrowID, participant string) {
	t.events.ExcludedHostServed(escrowID, participant)
}

// ForcedSend has no counter either: the burns it replaced are already counted, one by one, as ghosts.
func (t tracedDispatches) ForcedSend(escrowID, participant string, burnsInARow int64) {
	t.events.ForcedSend(escrowID, participant, burnsInARow)
}

// NonceHeld and EscrowRetired pass straight through: both are already written down where they happen.
func (t tracedDispatches) NonceHeld(escrowID string) { t.recorder.NonceHeld(escrowID) }

func (t tracedDispatches) EscrowRetired(escrowID string) { t.recorder.EscrowRetired(escrowID) }

// phaseNarrator hands the journal a chain change only when something moved; the observer polls twelve times a minute.
type phaseNarrator struct {
	events  *journal.Journal
	mu      sync.Mutex
	started bool
	epoch   uint64
	phase   chain.EpochPhase
	blocked bool
	reason  chain.BlockReason
}

// phaseChange separates deciding from saying, so writing nothing when nothing moved can be tested.
type phaseChange struct {
	first bool
	epoch bool
	block bool
}

// advance is called synchronously on every publish, so it holds its lock only to compare and update.
func (n *phaseNarrator) advance(snapshot chain.PhaseSnapshot) phaseChange {
	n.mu.Lock()
	defer n.mu.Unlock()
	change := phaseChange{
		first: !n.started,
		epoch: snapshot.EpochIndex != n.epoch || snapshot.EpochPhase != n.phase,
		block: snapshot.RequestsBlocked != n.blocked || snapshot.BlockReason != n.reason,
	}
	n.started, n.epoch, n.phase = true, snapshot.EpochIndex, snapshot.EpochPhase
	n.blocked, n.reason = snapshot.RequestsBlocked, snapshot.BlockReason
	return change
}

func (n *phaseNarrator) observe(snapshot chain.PhaseSnapshot) {
	change := n.advance(snapshot)
	if change.first || change.epoch {
		n.events.ChainEpoch(snapshot.EpochIndex, snapshot.EpochPhase, snapshot.BlockHeight, snapshot.EpochSwitchBlockHeight)
	}
	if !change.first && !change.block {
		return
	}
	if snapshot.RequestsBlocked {
		n.events.ChainRequestsBlocked(snapshot.BlockReason, snapshot.EpochIndex, snapshot.BlockHeight)
		return
	}
	if !change.first {
		n.events.ChainRequestsUnblocked(snapshot.EpochIndex, snapshot.BlockHeight)
	}
}

// nonceAccountedRaces hands one race outcome to both readers of it. See ../README.md, "Two readers of one fact".
type nonceAccountedRaces struct {
	recorder *metrics.RaceRecorder
	events   *journal.Journal
}

func (r nonceAccountedRaces) RecordRace(outcome engine.RaceOutcome) {
	r.recorder.RecordRace(outcome)
	r.events.RecordRace(outcome)
}

// RecordAwaitedFinish passes straight through: the ledger took the attempt with its race.
func (r nonceAccountedRaces) RecordAwaitedFinish(outcome engine.RaceOutcome, attempt engine.AttemptOutcome) {
	r.recorder.RecordAwaitedFinish(outcome, attempt)
}

func (r nonceAccountedRaces) RecordTimeout(event engine.TimeoutEvent) {
	r.recorder.RecordTimeout(event)
	r.events.RecordTimeout(event)
}

// RecordClassifyOverflow passes straight through: an overflowing classifier says nothing about a nonce.
func (r nonceAccountedRaces) RecordClassifyOverflow(participant, model string) {
	r.recorder.RecordClassifyOverflow(participant, model)
}

// probeVotes counts a warmup vote like a race vote and hands it to the ledger without a race vote's line. See ../README.md, "Two readers of one fact".
type probeVotes struct {
	recorder *metrics.RaceRecorder
	events   *journal.Journal
}

func (p probeVotes) RecordTimeout(event engine.TimeoutEvent) {
	p.recorder.RecordTimeout(event)
	p.events.RecordProbeTimeout(event)
}

// journalSettings keeps a disabled ledger out of the interface field: a typed nil there is non-nil to a nil check.
func journalSettings(recorder *nonces.Recorder) journal.Settings {
	if recorder == nil {
		return journal.Settings{}
	}
	return journal.Settings{Ledger: recorder}
}

// journalCounts adapts the journal's counters to the collector, which cannot import journal.
func journalCounts(events *journal.Journal) func() metrics.JournalCounts {
	return func() metrics.JournalCounts {
		moneyRefused, progressDropped, lateEvents := events.Counts()
		return metrics.JournalCounts{MoneyRefused: moneyRefused, ProgressDropped: progressDropped, LateEvents: lateEvents}
	}
}

package scheduler

import (
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/limits"
)

// RequestProfile is one request as routing reads it; RequestID only names it on the burns it causes, and Params must be exactly devshard/user.InferenceParams. See README, "The boundary types".
type RequestProfile struct {
	RequestID         string
	Model             string
	Escrow            string
	InputTokens       int
	InputTokensSource filters.InputTokensSource
	InputBytes        int
	OutputTokens      int
	Exclude           []string
	Params            any
	// Escalation marks a speculative attempt of a race that already holds Escrow. See routing.md, "Picking an escrow".
	Escalation bool
}

// Burn is a committed nonce the scheduler spent on nobody, and the request it was spent during.
type Burn struct {
	Nonce       uint64
	Participant string
	Reason      string
	RequestID   string
}

// Assignment is a committed nonce ready to spend. See README, "The boundary types".
type Assignment struct {
	Escrow     string
	Host       string
	Nonce      Prepared
	EscrowHold func()
	HostSlot   func()
}

// ReleaseEscrow gives the hold back: as soon as the caller has its own, or instead of dispatching.
func (a Assignment) ReleaseEscrow() {
	if a.EscrowHold != nil {
		a.EscrowHold()
	}
}

// ReleaseHostSlot gives the host's congestion tokens back: when the attempt ends, or instead of dispatching.
func (a Assignment) ReleaseHostSlot() {
	if a.HostSlot != nil {
		a.HostSlot()
	}
}

// escrowSource is the candidate-escrow registry; Candidates returns a stable, already-filtered order.
type escrowSource interface {
	Candidates(model string) []Escrow
	// RetiredAtNonceCap is an escrow out of routing for its nonce cap that a race still holds; only that race's escalations may use it.
	RetiredAtNonceCap(escrowID string) (Escrow, bool)
}

// Escrow is one candidate; a nil Hold counts nothing. See README, "The boundary types".
type Escrow struct {
	ID          string
	Model       string
	SessionID   uint64
	Session     session
	ActiveUsers int
	IsReserve   bool
	Hold        func() (release func(), ok bool)
	// EscalationHold is Hold for a queue of escalations alone, and also holds an escrow RetiredAtNonceCap would return.
	EscalationHold func() (release func(), ok bool)
}

// NonceIntent is what the scheduler tells a session to do with the nonce it offers. See README, "The boundary types".
type NonceIntent struct {
	Commit bool
	Ghost  bool
	Params any
}

// session is the narrow view of devshard/user.Session the scheduler needs; Advance is the atomic peek->decide->commit unit. See README, "The boundary types".
type session interface {
	Advance(decide func(HostBinding) NonceIntent) (Prepared, error)
	ParticipantKeys() []string               // distinct participants (slots deduped) -- the exclusion universe
	SlotParticipants() []string              // one per slot, duplicates kept, read-only; nonce % GroupSize indexes it
	GroupSize() int                          // len(group); nonce % GroupSize == hostIdx
	LatestNonce() uint64                     // for the nonce-cap gate and the burn forecast
	Balance() uint64                         // for the balance floor
	TokenPrice() uint64                      // for the balance floor
	FeePerNonce() uint64                     // for the balance floor
	ChatBudgetSpent(participant string) bool // the host's advertised peer budget has no chat in it now
}

// HostBinding is the nonce the session is offering and the host it is bound to, deduped across a validator's slots.
type HostBinding struct {
	Nonce       uint64
	HostIdx     int
	Participant string
}

// Prepared is a committed nonce ready for dispatch; nil means the nonce was declined.
type Prepared interface {
	Nonce() uint64
	HostIdx() int
}

// snapshotSource is satisfied by *chain.PhaseObserver.
type snapshotSource interface{ Snapshot() chain.PhaseSnapshot }

// escrowWeights is satisfied by *limits.Capacity.
type escrowWeights interface {
	EscrowWeight(escrowID, model string) float64
}

// hostLimiter is satisfied by *limits.ParticipantLimiter. See README, "The boundary types".
type hostLimiter interface {
	Admits(participant, model string) limits.Admission
	Acquire(participant, model string, cost limits.TokenCost) (func(), limits.Admission)
	Overdraft(participant, model string, cost limits.TokenCost) (func(), limits.Admission)
}

// hostHealth is satisfied by *perf.Tracker; Ejected is already capped, so honouring it cannot empty the pool.
type hostHealth interface {
	Ejected(participant, model string) bool
}

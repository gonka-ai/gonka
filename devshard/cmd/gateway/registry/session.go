package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"devshard/cmd/gateway/scheduler"
	"devshard/heightsync"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

const ghostMaxTokens = uint64(state.MinReservationTokens)

// See README.md, "Nonces and ghost burns".
var (
	ghostPrompt = fmt.Appendf(nil, `{"messages":[{"role":"user","content":"."}],"max_tokens":%d}`, ghostMaxTokens)

	errNonceDeclined = errors.New("nonce declined")
)

// HostDial is where one of an escrow's hosts answers. See README.md.
type HostDial struct {
	ParticipantKey string
	BaseURL        string
	RoutePrefix    string
}

// ChatBudget is a host connection that reports how long a chat would wait for the peer budget the host advertised.
type ChatBudget interface {
	ChatBudgetWait() time.Duration
}

// EscrowSession is one escrow's session as the registry uses it; two of its methods carry a trap. See README.md, "The two session kinds".
type EscrowSession interface {
	ParticipantKeys() []string
	HostDials() []HostDial
	ChatBudgets() map[string]ChatBudget
	HeightSyncView() heightsync.OperatorView
	WaitRouterCatalog(ctx context.Context) error
	WaitHeightSeedReady(ctx context.Context) error
	HostParticipantKeyList() []string
	Nonce() uint64
	PublishedNonce() uint64
	Balance() uint64
	TokenPrice() uint64
	FeePerNonce() uint64
	Phase() types.SessionPhase
	PrepareInferenceFn(chooser user.ParamsForHost) (*user.PreparedInference, error)
	SettlementSignatures(nonce uint64) (map[uint32][]byte, error)
	SignaturesAt(nonce uint64) map[uint32][]byte
	SignedSlots() map[uint64]types.Bitmap128
	SignatureStatus() (entries []user.SignatureStatusEntry, highestQuorum uint64, hasAny bool)
	SnapshotState() types.EscrowState
	AllInferences() map[uint64]types.InferenceRecord
	Inference(id uint64) (types.InferenceRecord, bool)
	SealedInference(id uint64) (types.InferenceRecord, bool)
	ProtocolVersion() string
	LiveInferences() (types.SessionConfig, []types.InferenceRecord)
	SealedInferences() int
	SetChainMaxNonce(maxNonce uint32)
	ArmQuietSettle()
	PendingTxs() []*types.DevshardTx
	SendPendingDiff(ctx context.Context) error
	Finalize(ctx context.Context) error
	FlushSnapshot() error
	Close() error
	UserSession() *user.Session
	SweepExecutionTimeouts(ctx context.Context, grace time.Duration, budget int) user.SweepReport
	SweepBeforeFinalize(ctx context.Context, budget int) (user.SweepReport, error)
}

// SessionFactory: an escrow with no record must fail wrapping escrow.ErrUnknownEscrow. See README.md, "The two session kinds".
type SessionFactory func(ctx context.Context, escrowID string) (EscrowSession, error)

type sessionHandle struct {
	*user.Session
	machine         *state.StateMachine
	finalizing      finalizeLock
	protocolVersion string
}

// NewSessionHandle takes the protocol version once. See README.md, "The two session kinds".
func NewSessionHandle(session *user.Session, machine *state.StateMachine) EscrowSession {
	return sessionHandle{
		Session:         session,
		machine:         machine,
		finalizing:      newFinalizeLock(),
		protocolVersion: machine.SnapshotStateNoInferences().StateRootAndProtocolVersion,
	}
}

func (h sessionHandle) Finalize(ctx context.Context) error {
	if err := h.finalizing.LockContext(ctx); err != nil {
		return err
	}
	defer h.finalizing.Unlock()
	return h.Session.Finalize(ctx)
}

// SweepExecutionTimeouts steps over an escrow a Finalize holds instead of waiting it out, and votes only while the session is active.
func (h sessionHandle) SweepExecutionTimeouts(ctx context.Context, grace time.Duration, budget int) user.SweepReport {
	if !h.finalizing.TryLock() {
		return user.SweepReport{}
	}
	defer h.finalizing.Unlock()
	if h.machine.Phase() != types.PhaseActive {
		return user.SweepReport{}
	}
	return h.Session.SweepExecutionTimeouts(ctx, grace, budget)
}

// SweepBeforeFinalize waits out a sweep or Finalize holding the escrow, then votes with no race grace while the session is active.
func (h sessionHandle) SweepBeforeFinalize(ctx context.Context, budget int) (user.SweepReport, error) {
	if err := h.finalizing.LockContext(ctx); err != nil {
		return user.SweepReport{}, err
	}
	defer h.finalizing.Unlock()
	if h.machine.Phase() != types.PhaseActive {
		return user.SweepReport{}, nil
	}
	return h.Session.SweepExecutionTimeouts(ctx, noRaceGrace, budget), nil
}

type finalizeLock chan struct{}

func newFinalizeLock() finalizeLock { return make(finalizeLock, 1) }

func (l finalizeLock) Lock() { l <- struct{}{} }

func (l finalizeLock) Unlock() {
	select {
	case <-l:
	default:
	}
}

func (l finalizeLock) TryLock() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l finalizeLock) LockContext(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h sessionHandle) Phase() types.SessionPhase        { return h.machine.Phase() }
func (h sessionHandle) SnapshotState() types.EscrowState { return h.machine.SnapshotState() }
func (h sessionHandle) ProtocolVersion() string          { return h.protocolVersion }
func (h sessionHandle) SealedInferences() int            { return len(h.machine.ExportSealedNonces()) }
func (h sessionHandle) UserSession() *user.Session       { return h.Session }

func (h sessionHandle) LiveInferences() (types.SessionConfig, []types.InferenceRecord) {
	ids := h.machine.LiveInferenceIDs()
	records := make([]types.InferenceRecord, 0, len(ids))
	for id := range ids {
		if record, tracked := h.machine.GetInference(id); tracked {
			records = append(records, record)
		}
	}
	return h.machine.Config(), records
}

func (h sessionHandle) SessionConfig() types.SessionConfig    { return h.machine.Config() }
func (h sessionHandle) AppliedNonce() uint64                  { return h.machine.LatestNonce() }
func (h sessionHandle) LiveInferenceIDs() map[uint64]struct{} { return h.machine.LiveInferenceIDs() }

func (h sessionHandle) Inference(id uint64) (types.InferenceRecord, bool) {
	return h.machine.GetInference(id)
}

func (h sessionHandle) SealedInference(id uint64) (types.InferenceRecord, bool) {
	return h.machine.LookupSealedInference(id)
}

// AllInferences reads sealed records back from storage beside the live ones, so it is for inspection, never the request path.
func (h sessionHandle) AllInferences() map[uint64]types.InferenceRecord {
	return h.machine.ExportAllInferenceRecords()
}

type hostDialer interface {
	BaseURL() string
	RoutePrefix() string
}

// HeightSyncView serves the cache only: taking a fresh snapshot on a debug read would move the
// producer's own state. An unwired session reports nothing rather than an empty shape.
func (h sessionHandle) HeightSyncView() heightsync.OperatorView {
	if !h.HeightSyncWired() {
		return heightsync.OperatorView{}
	}
	return h.CachedHeightSyncView()
}

func (h sessionHandle) HostDials() []HostDial {
	clients := h.Clients()
	keys := h.HostParticipantKeyList()
	dials := make([]HostDial, 0, len(clients))
	for slot, client := range clients {
		dialer, addressable := client.(hostDialer)
		if !addressable || dialer == nil {
			continue
		}
		key := ""
		if slot < len(keys) {
			key = keys[slot]
		}
		dials = append(dials, HostDial{ParticipantKey: key, BaseURL: dialer.BaseURL(), RoutePrefix: dialer.RoutePrefix()})
	}
	return dials
}

func (h sessionHandle) ChatBudgets() map[string]ChatBudget {
	clients := h.Clients()
	keys := h.HostParticipantKeyList()
	budgets := make(map[string]ChatBudget, len(clients))
	for slot, client := range clients {
		budget, paced := client.(ChatBudget)
		if !paced || slot >= len(keys) {
			continue
		}
		budgets[keys[slot]] = budget
	}
	return budgets
}

type nonceStream struct {
	session      EscrowSession
	model        string
	slots        []string
	participants []string
	budgets      map[string]ChatBudget
	now          func() time.Time
}

// newNonceStream is the only way to build one, so the cached group cannot disagree with the session's.
func newNonceStream(session EscrowSession, model string, now func() time.Time) nonceStream {
	return nonceStream{
		session: session, model: model, now: now,
		slots: session.HostParticipantKeyList(), participants: session.ParticipantKeys(), budgets: session.ChatBudgets(),
	}
}

func (s nonceStream) ParticipantKeys() []string  { return s.participants }
func (s nonceStream) SlotParticipants() []string { return s.slots }
func (s nonceStream) GroupSize() int             { return len(s.slots) }
func (s nonceStream) LatestNonce() uint64        { return s.session.PublishedNonce() }
func (s nonceStream) Balance() uint64            { return s.session.Balance() }
func (s nonceStream) TokenPrice() uint64         { return s.session.TokenPrice() }
func (s nonceStream) FeePerNonce() uint64        { return s.session.FeePerNonce() }

func (s nonceStream) ChatBudgetSpent(participant string) bool {
	budget, paced := s.budgets[participant]
	return paced && budget.ChatBudgetWait() > 0
}

func (s nonceStream) Advance(decide func(scheduler.HostBinding) scheduler.NonceIntent) (scheduler.Prepared, error) {
	var bound uint64
	prepared, err := s.session.PrepareInferenceFn(func(binding user.HostBinding) (user.InferenceParams, bool, context.Context, error) {
		bound = binding.Nonce
		intent := decide(scheduler.HostBinding{
			Nonce:       binding.Nonce,
			HostIdx:     binding.HostIdx,
			Participant: binding.ParticipantKey,
		})
		switch {
		case !intent.Commit:
			return user.InferenceParams{}, false, nil, errNonceDeclined
		case intent.Ghost:
			return s.ghostParams(), false, nil, nil
		}
		params, isInference := intent.Params.(user.InferenceParams)
		if !isInference {
			return user.InferenceParams{}, false, nil, fmt.Errorf("dispatch params are %T, want user.InferenceParams", intent.Params)
		}
		return params, false, nil, nil
	})
	switch {
	case errors.Is(err, errNonceDeclined):
		return nil, nil
	case errors.Is(err, user.ErrPreparedAfterCommit):
		return nil, scheduler.NewSpentNonceError(bound, err)
	case err != nil:
		return nil, err
	case prepared == nil:
		return nil, nil
	}
	return prepared, nil
}

// StartedAt is seconds, not milliseconds, or the refusal deadline goes negative. See host/timeout.go, VerifyRefusedTimeout.
func (s nonceStream) ghostParams() user.InferenceParams {
	return user.InferenceParams{
		Model:       s.model,
		Prompt:      ghostPrompt,
		InputLength: uint64(len(ghostPrompt)),
		MaxTokens:   ghostMaxTokens,
		StartedAt:   s.now().Unix(),
	}
}

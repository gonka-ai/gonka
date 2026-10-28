package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/host"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

// escrows is satisfied by *registry.Registry. See rules.md, "4. Routing and settlement read the escrow set asymmetrically".
type escrows interface {
	AcquireCommitted(escrowID string) (registry.EscrowSession, func(), bool)
	SettlementSession(escrowID string) (registry.EscrowSession, bool)
}

// Sessions adapts the live escrows to the engine's two outbound boundaries: the dispatch target and the vote poster.
type Sessions struct{ escrows escrows }

func NewSessions(escrows escrows) Sessions { return Sessions{escrows: escrows} }

// Target resolves one escrow per race; it reaches a draining one too, because the pick already committed a nonce that a rotation since must not strand.
func (s Sessions) Target(escrowID string) (engine.DispatchTarget, func(), bool) {
	session, release, held := s.escrows.AcquireCommitted(escrowID)
	if !held {
		return nil, nil, false
	}
	return escrowTarget{session: session.UserSession()}, release, true
}

// dispatchSession is one escrow's session as a race spends it; *user.Session satisfies it.
type dispatchSession interface {
	CatchUpBeforeSend(ctx context.Context, prepared *user.PreparedInference) error
	SendOnly(ctx context.Context, prepared *user.PreparedInference, stream io.Writer, onReceipt func()) (*host.HostResponse, error)
	ProcessResponse(hostIdx int, reply *host.HostResponse, inferenceNonce uint64) error
	BindReceivedStream(nonce uint64, received [][32]byte)
	PinPendingFinish(inferenceID uint64)
	UnpinPendingFinish(inferenceID uint64)
	ArmQuietSettle()
	IsNonceFinished(nonce uint64) bool
	HostParticipantKeyList() []string
	HostLabel(hostIdx int) string
	RewindHostCatchUp(hostIdx int, cause string) bool
}

type escrowTarget struct{ session dispatchSession }

var _ engine.DispatchTarget = escrowTarget{}

func (t escrowTarget) HostCount() int               { return len(t.session.HostParticipantKeyList()) }
func (t escrowTarget) HostLabel(hostIdx int) string { return t.session.HostLabel(hostIdx) }

func (t escrowTarget) RewindHostCatchUp(hostIdx int, cause string) bool {
	return t.session.RewindHostCatchUp(hostIdx, cause)
}
func (t escrowTarget) NonceFinished(nonce uint64) bool { return t.session.IsNonceFinished(nonce) }

func (t escrowTarget) CatchUp(ctx context.Context, nonce scheduler.Prepared, onFault func()) error {
	prepared, err := preparedInference(nonce)
	if err != nil {
		return err
	}
	prepared.OnCatchUpFault(onFault)
	return t.session.CatchUpBeforeSend(ctx, prepared)
}

func preparedInference(nonce scheduler.Prepared) (*user.PreparedInference, error) {
	prepared, ok := nonce.(*user.PreparedInference)
	if !ok {
		return nil, fmt.Errorf("dispatch nonce is %T, want *user.PreparedInference", nonce)
	}
	return prepared, nil
}

func (t escrowTarget) Send(ctx context.Context, nonce scheduler.Prepared, stream io.Writer, onReceipt func()) (engine.Response, error) {
	prepared, err := preparedInference(nonce)
	if err != nil {
		return nil, err
	}
	// stream is handed on unwrapped: the transport type-asserts http.Flusher on it to flush each SSE line.
	reply, err := registry.SendRecovering(ctx, t.session, prepared, stream, onReceipt)
	if reply != nil {
		t.session.PinPendingFinish(prepared.Nonce())
		// Applies for a reply that arrived beside an error too, and keeps both: a failed send must not hide a diverged state. See rules.md.
		if applyErr := t.session.ProcessResponse(prepared.HostIdx(), reply, prepared.Nonce()); applyErr != nil {
			err = errors.Join(err, applyErr)
		}
		t.session.BindReceivedStream(prepared.Nonce(), reply.ReceivedResponseHashes)
	}
	switch {
	case divergedState(err):
		err = fmt.Errorf("%w: %w", engine.ErrStateRootDivergence, err)
	case errors.Is(err, user.ErrLocalRootUnavailable):
		err = fmt.Errorf("%w: %w", engine.ErrLocalRootUnavailable, err)
	}
	if reply == nil {
		return nil, err
	}
	inferenceID := prepared.Nonce()
	return hostReply{reply: reply, releaseFinish: sync.OnceFunc(func() {
		t.session.UnpinPendingFinish(inferenceID)
		t.session.ArmQuietSettle()
	})}, err
}

// divergedState reads a rejected post state root from either side of the wire: the host's diff or the local hash.
func divergedState(err error) bool {
	return state.IsPostStateRootMismatchError(err) || errors.Is(err, types.ErrStateHashMismatch)
}

type hostReply struct {
	reply         *host.HostResponse
	releaseFinish func()
}

var _ engine.Response = hostReply{}

func (r hostReply) Confirmed() bool { return r.reply.ConfirmedAt > 0 }

func (r hostReply) ConfirmedAt() int64 { return r.reply.ConfirmedAt }

func (r hostReply) ReleaseFinish() { r.releaseFinish() }

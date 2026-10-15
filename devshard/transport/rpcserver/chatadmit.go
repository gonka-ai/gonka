package rpcserver

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"devshard/observability"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// chatGate is one Chat's admission state. handshakeGate counts the Chat as
// pending and arms the read deadline before Connect reads the envelope.
// The stream slot and the weight are taken by admitChat, after the owner
// check, so a slot member or an unread envelope never holds a Chat slot.
type chatGate struct {
	auth *PeerAuthHandler
	peer string

	pending     bool
	pendingOnce sync.Once
	deadline    time.Time
	read        bool
}

type chatGateKey struct{}

func withChatGate(ctx context.Context, g *chatGate) context.Context {
	return context.WithValue(ctx, chatGateKey{}, g)
}

func chatGateFromContext(ctx context.Context) *chatGate {
	g, _ := ctx.Value(chatGateKey{}).(*chatGate)
	return g
}

// openChatGate runs in handshakeGate, before Connect reads the envelope.
// The caller must defer finish.
func openChatGate(ctx context.Context, auth *PeerAuthHandler, w http.ResponseWriter) (*chatGate, error) {
	peer := PeerFromContext(ctx)
	if err := auth.limiter.acquirePendingChat(ctx, peer); err != nil {
		observability.IncPeerRPCChatDropped(observability.PeerRPCChatDroppedPendingCap)
		return nil, err
	}
	g := &chatGate{auth: auth, peer: peer, pending: true}
	if d := auth.cfg.ChatReadTimeout; d > 0 {
		g.deadline = time.Now().Add(d)
		_ = setReadDeadline(w, g.deadline)
	}
	return g, nil
}

// finish runs when the Chat request ends. A Chat that never reached
// SessionHandler.Chat after its read deadline is a read timeout.
func (g *chatGate) finish() {
	g.releasePending()
	if !g.read && !g.deadline.IsZero() && !time.Now().Before(g.deadline) {
		observability.IncPeerRPCChatDropped(observability.PeerRPCChatDroppedReadTimeout)
	}
}

func (g *chatGate) releasePending() {
	if !g.pending {
		return
	}
	g.pendingOnce.Do(func() { g.auth.limiter.releasePendingChat(g.peer) })
}

// chatEnvelopeRead marks that Connect has read the envelope, so finish does
// not count this Chat as a read timeout. The read deadline can stay armed:
// it only fails reads of a body that is already at EOF.
func chatEnvelopeRead(ctx context.Context) {
	if g := chatGateFromContext(ctx); g != nil {
		g.read = true
	}
}

// admitChat takes the Chat stream slot, then the weight, for a peer that
// passed the owner check. A stream-cap refusal does not spend weight.
// release frees the slot; call it when the Chat ends.
func admitChat(ctx context.Context) (release func(), err error) {
	g := chatGateFromContext(ctx)
	if g == nil || g.auth == nil {
		return func() {}, nil
	}
	auth := g.auth
	peer := PeerFromContext(ctx)
	procedure := rpcpbconnect.SessionServiceChatProcedure
	if err := auth.limiter.acquireStream(ctx, peer, procedure); err != nil {
		auth.observeRPC(ctx, procedure, peer, true, true, false, false)
		return nil, err
	}
	if err := auth.limiter.charge(ctx, peer, procedure); err != nil {
		auth.limiter.releaseStream(peer, procedure)
		auth.observeRPC(ctx, procedure, peer, true, false, false, false)
		return nil, err
	}
	auth.observeRPC(ctx, procedure, peer, false, false, false, false)
	g.releasePending()
	return func() { auth.limiter.releaseStream(peer, procedure) }, nil
}

// sendChatFrame bounds one ChatFrame send by ChatSendTimeout. The deadline
// is cleared after Send so time spent waiting on the model is not counted.
func sendChatFrame(ctx context.Context, send func() error) error {
	g := chatGateFromContext(ctx)
	if g == nil || g.auth == nil || g.auth.cfg.ChatSendTimeout <= 0 {
		return send()
	}
	d := g.auth.cfg.ChatSendTimeout
	start := time.Now()
	if err := writeDeadline(ctx, start.Add(d)); err != nil {
		return err
	}
	if err := send(); err != nil {
		if time.Since(start) >= d {
			observability.IncPeerRPCChatDropped(observability.PeerRPCChatDroppedSendTimeout)
		}
		return err
	}
	return writeDeadline(ctx, time.Time{})
}

func setReadDeadline(w http.ResponseWriter, deadline time.Time) error {
	if w == nil {
		return nil
	}
	err := http.NewResponseController(w).SetReadDeadline(deadline)
	if err == nil || errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

package rpcserver

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
)

type clientIPKey struct{}

func withClientIP(ctx context.Context, ip string) context.Context {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ctx
	}
	return context.WithValue(ctx, clientIPKey{}, ip)
}

func clientIPFromContext(ctx context.Context) string {
	v, _ := ctx.Value(clientIPKey{}).(string)
	return v
}

func (h *PeerAuthHandler) observeRPC(ctx context.Context, procedure, peer string, banned, streamCap, attach, attachFloor bool) {
	h.observeRPCSample(ctx, procedure, peer, banned, streamCap, attach, attachFloor, false)
}

func (h *PeerAuthHandler) observeRPCSample(ctx context.Context, procedure, peer string, banned, streamCap, attach, attachFloor, attachOrigin bool) {
	if h == nil || h.traffic == nil {
		return
	}
	escrow := ""
	if !attach {
		escrow = h.observeEscrowID(ctx)
	}
	ip := ""
	if !attach {
		ip = clientIPFromContext(ctx)
	}
	h.traffic.Observe(ctx, transport.RPCSample{
		Procedure:    procedure,
		Peer:         peer,
		Escrow:       escrow,
		IP:           ip,
		Banned:       banned,
		Attach:       attach,
		AttachFloor:  attachFloor,
		AttachOrigin: attachOrigin,
		StreamCap:    streamCap,
	})
}

// observeEscrowID is the shard key: a live local session, not the raw URL.
func (h *PeerAuthHandler) observeEscrowID(ctx context.Context) string {
	id := EscrowIDFromContext(ctx)
	if id == "" || id == transport.HostRPCEscrowID {
		return ""
	}
	if h.cfg.LiveSession == nil || !h.cfg.LiveSession(id) {
		return ""
	}
	return id
}

func (h *PeerAuthHandler) observeAttach(ctx context.Context, err error) {
	banned := err != nil && connect.CodeOf(err) == connect.CodeResourceExhausted
	origin := false
	floor := false
	if banned {
		msg := err.Error()
		// The origin sentence contains the process sentence, so the longer
		// one is checked first. Otherwise every origin refusal is a floor hit.
		origin = strings.Contains(msg, attachOriginLimitMessage)
		floor = !origin && strings.Contains(msg, attachProcessLimitMessage)
	}
	h.observeRPCSample(ctx, rpcpbconnect.PeerAuthServiceAttachProcedure, "", banned, false, true, floor, origin)
}

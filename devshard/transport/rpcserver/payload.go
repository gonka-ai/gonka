package rpcserver

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"devshard/storage"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

const rpcGetPayloadRoute = "rpc_get_payload"

// GetPayloadFunc is HostManager.ServeRPCGetPayload. Lookup and group
// membership are already checked; srv is the resolved session.
type GetPayloadFunc func(ctx context.Context, srv SessionCore, peer string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error)

// UnboundGetPayloadFunc serves GetPayload when this host has no session row.
// The implementation loads the chain roster; it must not CreateSession.
type UnboundGetPayloadFunc func(ctx context.Context, peer string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error)

// PayloadHandler implements PayloadService.
type PayloadHandler struct {
	rpcpbconnect.UnimplementedPayloadServiceHandler
	sessionResolver
	serve   GetPayloadFunc
	unbound UnboundGetPayloadFunc
}

func NewPayloadHandler(lookup SessionLookup, serve GetPayloadFunc) *PayloadHandler {
	return &PayloadHandler{sessionResolver: newSessionResolver(lookup), serve: serve}
}

// SetUnboundGetPayload enables the chain-roster path when SessionServerExisting misses.
func (h *PayloadHandler) SetUnboundGetPayload(fn UnboundGetPayloadFunc) {
	if h == nil {
		return
	}
	h.unbound = fn
}

func (h *PayloadHandler) GetPayload(ctx context.Context, req *connect.Request[rpcpb.GetPayloadRequest]) (*connect.Response[rpcpb.GetPayloadResponse], error) {
	if h.serve == nil {
		return nil, unimplementedCore()
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	if h.unbound == nil {
		return h.getPayloadBound(ctx, req)
	}
	return h.getPayloadWithRoster(ctx, req)
}

func (h *PayloadHandler) getPayloadBound(ctx context.Context, req *connect.Request[rpcpb.GetPayloadRequest]) (*connect.Response[rpcpb.GetPayloadResponse], error) {
	peer, _, srv, err := h.resolve(ctx, rpcGetPayloadRoute)
	if err != nil {
		return nil, err
	}
	if err := requireGroupMember(srv, peer); err != nil {
		return nil, err
	}
	return h.finishGetPayload(ctx, srv, peer, req)
}

func (h *PayloadHandler) getPayloadWithRoster(ctx context.Context, req *connect.Request[rpcpb.GetPayloadRequest]) (*connect.Response[rpcpb.GetPayloadResponse], error) {
	peer, escrow, err := requirePeer(ctx)
	if err != nil {
		return nil, err
	}
	if h.lookup == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session lookup not configured"))
	}
	srv, serr := h.lookup.SessionServerExisting(escrow)
	if serr == nil && srv != nil {
		recordRPCSessionResolution(ctx, rpcGetPayloadRoute, escrow, nil)
		if !srv.AllowsSender(peer) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
		}
		if err := requireGroupMember(srv, peer); err != nil {
			return nil, err
		}
		return h.finishGetPayload(ctx, srv, peer, req)
	}
	if serr != nil && !errors.Is(serr, storage.ErrSessionNotFound) {
		recordRPCSessionResolution(ctx, rpcGetPayloadRoute, escrow, serr)
		return nil, mapAllowError(serr)
	}
	recordRPCSessionResolution(ctx, rpcGetPayloadRoute, escrow, storage.ErrSessionNotFound)
	resp, err := h.unbound(ctx, peer, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PayloadHandler) finishGetPayload(ctx context.Context, srv SessionCore, peer string, req *connect.Request[rpcpb.GetPayloadRequest]) (*connect.Response[rpcpb.GetPayloadResponse], error) {
	resp, err := h.serve(ctx, srv, peer, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

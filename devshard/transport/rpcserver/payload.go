package rpcserver

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

const rpcGetPayloadRoute = "rpc_get_payload"

// GetPayloadFunc is HostManager.ServeRPCGetPayload. Lookup and group
// membership are already checked; srv is the resolved session.
type GetPayloadFunc func(ctx context.Context, srv SessionCore, peer string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error)

// PayloadHandler implements PayloadService.
type PayloadHandler struct {
	rpcpbconnect.UnimplementedPayloadServiceHandler
	sessionResolver
	serve GetPayloadFunc
}

func NewPayloadHandler(lookup SessionLookup, serve GetPayloadFunc) *PayloadHandler {
	return &PayloadHandler{sessionResolver: newSessionResolver(lookup), serve: serve}
}

func (h *PayloadHandler) GetPayload(ctx context.Context, req *connect.Request[rpcpb.GetPayloadRequest]) (*connect.Response[rpcpb.GetPayloadResponse], error) {
	if h.serve == nil {
		return nil, unimplementedCore()
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, _, srv, err := h.resolve(ctx, rpcGetPayloadRoute)
	if err != nil {
		return nil, err
	}
	if err := requireGroupMember(srv, peer); err != nil {
		return nil, err
	}
	resp, err := h.serve(ctx, srv, peer, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

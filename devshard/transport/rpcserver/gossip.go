package rpcserver

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

// GossipHandler implements GossipService.
type GossipHandler struct {
	rpcpbconnect.UnimplementedGossipServiceHandler
	sessionResolver
}

func NewGossipHandler(lookup SessionLookup) *GossipHandler {
	return &GossipHandler{sessionResolver: newSessionResolver(lookup)}
}

func (h *GossipHandler) Nonce(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.GossipAck], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, payload, err := h.openSigned(ctx, req.Msg, "rpc_gossip_nonce")
	if err != nil {
		return nil, err
	}
	if err := requireGroupMember(srv, peer); err != nil {
		return nil, err
	}
	var inner rpcpb.GossipNonceRequest
	if err := unmarshalPayload(payload, &inner); err != nil {
		return nil, err
	}
	type nonceCore interface {
		ServeGossipNonce(transport.GossipNonceRequest) error
	}
	core, ok := srv.(nonceCore)
	if !ok {
		return nil, unimplementedCore()
	}
	if err := core.ServeGossipNonce(transport.GossipNonceRequestFromProto(&inner)); err != nil {
		return nil, mapGossipError(err)
	}
	return connect.NewResponse(&rpcpb.GossipAck{}), nil
}

func (h *GossipHandler) Txs(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.GossipAck], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, payload, err := h.openSigned(ctx, req.Msg, "rpc_gossip_txs")
	if err != nil {
		return nil, err
	}
	if err := requireGroupMember(srv, peer); err != nil {
		return nil, err
	}
	var inner rpcpb.GossipTxsRequest
	if err := unmarshalPayload(payload, &inner); err != nil {
		return nil, err
	}
	txs, err := transport.DevshardTxsFromBytes(inner.GetTxs())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decode txs: "+err.Error()))
	}
	type txsCore interface {
		ServeGossipTxs([]*types.DevshardTx)
	}
	core, ok := srv.(txsCore)
	if !ok {
		return nil, unimplementedCore()
	}
	core.ServeGossipTxs(txs)
	return connect.NewResponse(&rpcpb.GossipAck{}), nil
}

func mapGossipError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, transport.ErrGossipMissingStateSig) ||
		errors.Is(err, transport.ErrGossipInvalidSlot) ||
		errors.Is(err, transport.ErrGossipInvalidStateSig) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeAborted, err)
}

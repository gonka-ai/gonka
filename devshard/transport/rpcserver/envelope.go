package rpcserver

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"devshard/signing"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
)

type sessionResolver struct {
	lookup   SessionLookup
	verifier signing.Verifier
	now      func() int64
}

func newSessionResolver(lookup SessionLookup) sessionResolver {
	return sessionResolver{
		lookup:   lookup,
		verifier: signing.NewSecp256k1Verifier(),
		now:      func() int64 { return time.Now().Unix() },
	}
}

func (r sessionResolver) resolve(ctx context.Context, route string) (peer, escrow string, srv SessionCore, err error) {
	return r.resolveSession(ctx, route, false)
}

func (r sessionResolver) resolveParticipant(ctx context.Context, route string) (peer, escrow string, srv SessionCore, err error) {
	return r.resolveSession(ctx, route, true)
}

func (r sessionResolver) resolveSession(ctx context.Context, route string, bindParticipant bool) (peer, escrow string, srv SessionCore, err error) {
	peer, escrow, err = requirePeer(ctx)
	if err != nil {
		return "", "", nil, err
	}
	if r.lookup == nil {
		return "", "", nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session lookup not configured"))
	}
	if bindParticipant {
		srv, err = r.lookup.SessionForParticipant(escrow, peer)
	} else {
		srv, err = r.lookup.SessionServerExisting(escrow)
	}
	if err != nil {
		recordRPCSessionResolution(ctx, route, escrow, err)
		return "", "", nil, mapAllowError(err)
	}
	if srv == nil {
		if bindParticipant {
			recordRPCSessionResolution(ctx, route, escrow, storage.ErrSessionNotFound)
			return "", "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
		}
		recordRPCSessionResolution(ctx, route, escrow, storage.ErrSessionNotFound)
		return "", "", nil, mapAllowError(storage.ErrSessionNotFound)
	}
	recordRPCSessionResolution(ctx, route, escrow, nil)
	if !srv.AllowsSender(peer) {
		return "", "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
	}
	return peer, escrow, srv, nil
}

func (r sessionResolver) openSigned(ctx context.Context, env *rpcpb.SignedEnvelope, route string) (peer string, srv SessionCore, payload []byte, err error) {
	peer, escrow, srv, err := r.resolveParticipant(ctx, route)
	if err != nil {
		return "", nil, nil, err
	}
	if env == nil {
		return "", nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil signed envelope"))
	}
	if env.GetEscrowId() != escrow {
		return "", nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("escrow mismatch"))
	}
	addr, vErr := transport.VerifyEnvelope(r.verifier, env, r.now())
	if vErr != nil {
		return "", nil, nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid envelope signature"))
	}
	if addr != peer {
		return "", nil, nil, connect.NewError(connect.CodePermissionDenied, errors.New("envelope signer does not match handshake"))
	}
	return peer, srv, env.GetPayload(), nil
}

func unmarshalPayload(payload []byte, msg proto.Message) error {
	if err := proto.Unmarshal(payload, msg); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid payload"))
	}
	return nil
}

func unimplementedCore() error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("method is not implemented"))
}

func permissionDenied(msg string) error {
	return connect.NewError(connect.CodePermissionDenied, errors.New(msg))
}

func requireOwner(srv SessionCore, peer string) error {
	type ownerCore interface {
		IsOwner(string) bool
	}
	o, ok := srv.(ownerCore)
	if !ok {
		return unimplementedCore()
	}
	if !o.IsOwner(peer) {
		return permissionDenied("restricted to escrow owner")
	}
	return nil
}

func requireGroupMember(srv SessionCore, peer string) error {
	type groupCore interface {
		IsGroupMember(string) bool
	}
	g, ok := srv.(groupCore)
	if !ok {
		return unimplementedCore()
	}
	if !g.IsGroupMember(peer) {
		return permissionDenied("restricted to group members")
	}
	return nil
}

func requireOwnerOrGroup(srv SessionCore, peer string) error {
	type ownerCore interface {
		IsOwner(string) bool
	}
	type groupCore interface {
		IsGroupMember(string) bool
	}
	o, hasOwner := srv.(ownerCore)
	g, hasGroup := srv.(groupCore)
	if !hasOwner && !hasGroup {
		return unimplementedCore()
	}
	if hasOwner && o.IsOwner(peer) {
		return nil
	}
	if hasGroup && g.IsGroupMember(peer) {
		return nil
	}
	return permissionDenied("restricted to escrow owner or group member")
}

package rpcserver

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"devshard/bridge"
	"devshard/observability"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// SessionCore is the transport-neutral surface SessionService needs.
// *transport.Server implements it.
type SessionCore interface {
	ServeGetSignatures(nonce uint64) (map[uint32][]byte, error)
	AllowsSender(address string) bool
}

// SessionLookup resolves a per-escrow session. HostManager is adapted via AdaptLookup.
type SessionLookup interface {
	SessionServerExisting(escrowID string) (SessionCore, error)
}

// AdaptLookup wraps a *transport.Server finder (HostManager.SessionServerExisting).
func AdaptLookup(fn func(escrowID string) (*transport.Server, error)) SessionLookup {
	return lookupAdapter(fn)
}

type lookupAdapter func(string) (*transport.Server, error)

func (f lookupAdapter) SessionServerExisting(id string) (SessionCore, error) {
	if f == nil {
		return nil, errors.New("session lookup not configured")
	}
	srv, err := f(id)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, nil
	}
	return srv, nil
}

// SessionHandler implements SessionService. Only GetSignatures is implemented.
type SessionHandler struct {
	rpcpbconnect.UnimplementedSessionServiceHandler
	lookup SessionLookup
}

func NewSessionHandler(lookup SessionLookup) *SessionHandler {
	return &SessionHandler{lookup: lookup}
}

func (h *SessionHandler) GetSignatures(ctx context.Context, req *connect.Request[rpcpb.GetSignaturesRequest]) (*connect.Response[rpcpb.GetSignaturesResponse], error) {
	if h.lookup == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session lookup not configured"))
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, escrowID, err := requirePeer(ctx)
	if err != nil {
		return nil, err
	}
	srv, err := h.lookup.SessionServerExisting(escrowID)
	if err != nil {
		recordRPCSessionResolution(ctx, escrowID, err)
		return nil, mapAllowError(err)
	}
	if srv == nil {
		recordRPCSessionResolution(ctx, escrowID, storage.ErrSessionNotFound)
		return nil, mapAllowError(storage.ErrSessionNotFound)
	}
	recordRPCSessionResolution(ctx, escrowID, nil)
	if !srv.AllowsSender(peer) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
	}
	sigs, err := srv.ServeGetSignatures(req.Msg.GetNonce())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("get signatures failed"))
	}
	if sigs == nil {
		sigs = map[uint32][]byte{}
	}
	return connect.NewResponse(&rpcpb.GetSignaturesResponse{Signatures: sigs}), nil
}

// mapAllowError is JSON sessionHTTPError on the Connect path. Wire strings are
// stable; codes match the HTTP status class.
func mapAllowError(err error) error {
	if isTransientSessionError(err) {
		return withDevshardError(hostInitializing(), transport.DevshardErrorInitializing)
	}
	if errors.Is(err, bridge.ErrChainUnavailable) {
		return withDevshardError(
			connect.NewError(connect.CodeUnavailable, errors.New("chain unavailable")),
			transport.DevshardErrorChainUnavailable,
		)
	}
	if errors.Is(err, storage.ErrSessionNotFound) {
		return connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if errors.Is(err, storage.ErrSessionNotActive) || errors.Is(err, bridge.ErrEscrowSettled) {
		return withDevshardError(
			connect.NewError(connect.CodeFailedPrecondition, errors.New("escrow settled")),
			transport.DevshardErrorEscrowSettled,
		)
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("session version conflict"))
	}
	if errors.Is(err, storage.ErrSessionEpochConflict) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("session epoch conflict"))
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("escrow is not open on this host"))
}

func hostInitializing() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("host initializing"))
}

func withDevshardError(err error, code string) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		ce.Meta().Set(transport.HeaderDevshardError, code)
		return ce
	}
	return err
}

// isTransientSessionError is the JSON 503 initializing set. server.ErrInitializing
// is the same class but lives in server (import cycle); production resolution
// returns storage.ErrStorageIndexRebuilding from GetSessionMeta.
func isTransientSessionError(err error) bool {
	return errors.Is(err, storage.ErrStorageIndexRebuilding)
}

const rpcGetSignaturesRoute = "rpc_get_signatures"

func recordRPCSessionResolution(ctx context.Context, escrowID string, err error) {
	status, reason := rpcResolutionStatus(err)
	observability.IncSessionResolution(rpcGetSignaturesRoute, status, reason)
	if err != nil {
		observability.Log(ctx, observability.LevelWarn, "devshard session resolution failed",
			observability.StageSessionResolved, observability.WhereRoutesSessionResolve, escrowID, reason, err)
	}
}

func rpcResolutionStatus(err error) (observability.MetricStatus, observability.Reason) {
	if err == nil {
		return observability.MetricStatusOK, observability.ReasonOK
	}
	if errors.Is(err, storage.ErrStorageIndexRebuilding) {
		return observability.MetricStatusError, observability.ReasonInitializing
	}
	if errors.Is(err, bridge.ErrChainUnavailable) {
		return observability.MetricStatusError, observability.ReasonGetEscrowErr
	}
	if errors.Is(err, storage.ErrSessionNotFound) {
		return observability.MetricStatusError, observability.ReasonSessionResolveErr
	}
	if errors.Is(err, storage.ErrSessionNotActive) || errors.Is(err, bridge.ErrEscrowSettled) {
		return observability.MetricStatusError, observability.ReasonEscrowSettled
	}
	if errors.Is(err, storage.ErrSessionVersionConflict) {
		return observability.MetricStatusError, observability.ReasonVersionConflict
	}
	if errors.Is(err, storage.ErrSessionEpochConflict) {
		return observability.MetricStatusError, observability.ReasonEpochConflict
	}
	return observability.MetricStatusError, observability.ReasonSessionResolveErr
}

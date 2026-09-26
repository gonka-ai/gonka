package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"

	"devshard"
	"devshard/bridge"
	"devshard/heightsync"
	"devshard/observability"
	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/types"
)

// SessionCore is the transport-neutral surface SessionService needs for
// handshake roster checks and GetSignatures. Extra ServeX methods are
// type-asserted so test stubs stay small.
type SessionCore interface {
	ServeGetSignatures(nonce uint64) (map[uint32][]byte, error)
	AllowsSender(address string) bool
}

// SessionLookup resolves a per-escrow session.
// SessionServerExisting must not CreateSession (observability GETs).
// SessionForParticipant never CreateSession (gossip / repair on a live row).
// SessionForOwner CreateSession only when addr is the escrow creator (Chat, seed).
// SessionForStartProof CreateSession when diffs carry a creator-signed
// MsgStartInference whose protocol_version matches this child.
type SessionLookup interface {
	SessionServerExisting(escrowID string) (SessionCore, error)
	// SessionForParticipant returns a live session when addr is allowed.
	// It does not bind a new version. Strangers return (nil, nil).
	SessionForParticipant(escrowID, addr string) (SessionCore, error)
	// SessionForOwner is BindOwnerChat: Existing + owner, or CreateSession
	// only for the escrow creator. Slot members return (nil, nil).
	SessionForOwner(escrowID, addr string) (SessionCore, error)
	// SessionForStartProof is ChallengeReceipt bind: Existing, or CreateSession
	// when diffs prove the gateway start and version. claimedVersion, when set,
	// must match MsgStartInference.protocol_version.
	SessionForStartProof(escrowID, addr string, diffs []types.Diff, claimedVersion string) (SessionCore, error)
}

// AdaptLookup wraps a *transport.Server finder (HostManager.SessionServerExisting).
// SessionForParticipant, SessionForOwner, and SessionForStartProof fall back
// to Existing (tests and obs-only lookups). Slot-member Chat then 403s in
// requireOwner without CreateSession.
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

func (f lookupAdapter) SessionForParticipant(id, addr string) (SessionCore, error) {
	_ = addr
	return f.SessionServerExisting(id)
}

func (f lookupAdapter) SessionForOwner(id, addr string) (SessionCore, error) {
	_ = addr
	return f.SessionServerExisting(id)
}

func (f lookupAdapter) SessionForStartProof(id, addr string, diffs []types.Diff, claimedVersion string) (SessionCore, error) {
	_ = addr
	_ = diffs
	_ = claimedVersion
	return f.SessionServerExisting(id)
}

// SessionHandler implements SessionService.
type SessionHandler struct {
	rpcpbconnect.UnimplementedSessionServiceHandler
	sessionResolver
}

func NewSessionHandler(lookup SessionLookup) *SessionHandler {
	return &SessionHandler{sessionResolver: newSessionResolver(lookup)}
}

func (h *SessionHandler) Chat(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope], stream *connect.ServerStream[rpcpb.ChatFrame]) (err error) {
	if req == nil || req.Msg == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, payload, err := h.openSignedOwner(ctx, req.Msg, "rpc_chat")
	if err != nil {
		return err
	}
	type inferenceCore interface {
		ServeInference(context.Context, transport.InferenceCall) error
	}
	core, ok := srv.(inferenceCore)
	if !ok {
		return unimplementedCore()
	}
	escrow := EscrowIDFromContext(ctx)
	ctx, op := observability.Request.StartInference(ctx, escrow, "")
	defer op.FinishErr(&err)
	observability.Request.SetEscrowID(op, escrow)
	observability.Request.SetSender(op, peer)
	observability.Request.SetInferenceBodyBytes(op, len(payload))

	sink := transport.NewChatFrameSink(func(chunk []byte) error {
		return stream.Send(&rpcpb.ChatFrame{Chunk: chunk})
	})
	err = mapInferenceError(core.ServeInference(ctx, transport.InferenceCall{
		SessionID: escrow,
		Sender:    peer,
		Body:      payload,
		Source:    "RPC Chat",
		Evidence: &heightsync.RequestLegEvidence{
			Body:      payload,
			Sig:       req.Msg.GetSignature(),
			Timestamp: req.Msg.GetTimestamp(),
			EscrowID:  escrow,
		},
		Sink: sink,
		Op:   op,
	}))
	if closeErr := sink.Close(); err == nil && closeErr != nil {
		err = mapInferenceError(closeErr)
	}
	return err
}

func (h *SessionHandler) GetSignatures(ctx context.Context, req *connect.Request[rpcpb.GetSignaturesRequest]) (*connect.Response[rpcpb.GetSignaturesResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	_, _, srv, err := h.resolve(ctx, "rpc_get_signatures")
	if err != nil {
		return nil, err
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

func (h *SessionHandler) GetDiffs(ctx context.Context, req *connect.Request[rpcpb.GetDiffsRequest]) (*connect.Response[rpcpb.GetDiffsResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	_, _, srv, err := h.resolve(ctx, "rpc_get_diffs")
	if err != nil {
		return nil, err
	}
	type diffsCore interface {
		ServeGetDiffs(from, to uint64) ([]types.DiffRecord, error)
	}
	core, ok := srv.(diffsCore)
	if !ok {
		return nil, unimplementedCore()
	}
	records, err := core.ServeGetDiffs(req.Msg.GetFrom(), req.Msg.GetTo())
	if err != nil {
		return nil, mapCoreError(err)
	}
	out := make([]*rpcpb.DiffRecord, len(records))
	for i, rec := range records {
		pb, encErr := transport.DiffToProto(rec.Diff)
		if encErr != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("encode diff failed"))
		}
		out[i] = &rpcpb.DiffRecord{Diff: pb, StateHash: rec.StateHash}
	}
	return connect.NewResponse(&rpcpb.GetDiffsResponse{Records: out}), nil
}

func (h *SessionHandler) GetMempool(ctx context.Context, req *connect.Request[rpcpb.GetMempoolRequest]) (*connect.Response[rpcpb.GetMempoolResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	_, _, srv, err := h.resolve(ctx, "rpc_get_mempool")
	if err != nil {
		return nil, err
	}
	type mempoolCore interface {
		ServeGetMempool(context.Context) ([]*types.DevshardTx, error)
	}
	core, ok := srv.(mempoolCore)
	if !ok {
		return nil, unimplementedCore()
	}
	txs, err := core.ServeGetMempool(ctx)
	if err != nil {
		return nil, mapCoreError(err)
	}
	raw, err := transport.DevshardTxsToBytes(txs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("encode mempool failed"))
	}
	return connect.NewResponse(&rpcpb.GetMempoolResponse{Txs: raw}), nil
}

func (h *SessionHandler) SeedHeightSync(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.SeedHeightSyncResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, _, err := h.openSignedOwner(ctx, req.Msg, "rpc_seed_height_sync")
	if err != nil {
		return nil, err
	}
	if err := requireOwner(srv, peer); err != nil {
		return nil, err
	}
	type seedCore interface {
		ServeSeedHeightSync(context.Context) (*heightsync.HeightSyncSection, error)
	}
	core, ok := srv.(seedCore)
	if !ok {
		return nil, unimplementedCore()
	}
	sec, err := core.ServeSeedHeightSync(ctx)
	if err != nil {
		return nil, mapCoreError(err)
	}
	return connect.NewResponse(&rpcpb.SeedHeightSyncResponse{HeightSync: transport.HeightSyncSectionToProto(sec)}), nil
}

func (h *SessionHandler) RepairHeightSync(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.RepairResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, payload, err := h.openSigned(ctx, req.Msg, "rpc_repair_height_sync")
	if err != nil {
		return nil, err
	}
	if err := requireGroupMember(srv, peer); err != nil {
		return nil, err
	}
	var inner rpcpb.RepairRequest
	if err := unmarshalPayload(payload, &inner); err != nil {
		return nil, err
	}
	type repairCore interface {
		ServeHeightSyncRepair(context.Context, string, *heightsync.RepairRequest) (*heightsync.RepairResponse, error)
	}
	core, ok := srv.(repairCore)
	if !ok {
		return nil, unimplementedCore()
	}
	resp, err := core.ServeHeightSyncRepair(ctx, peer, transport.RepairRequestFromProto(&inner))
	if err != nil {
		return nil, mapCoreError(err)
	}
	return connect.NewResponse(transport.RepairResponseToProto(resp)), nil
}

func (h *SessionHandler) VerifyTimeout(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.VerifyTimeoutResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, payload, err := h.openSignedOwner(ctx, req.Msg, "rpc_verify_timeout")
	if err != nil {
		return nil, err
	}
	if err := requireOwner(srv, peer); err != nil {
		return nil, err
	}
	var inner rpcpb.VerifyTimeoutRequest
	if err := unmarshalPayload(payload, &inner); err != nil {
		return nil, err
	}
	type verifyCore interface {
		ServeVerifyTimeout(context.Context, transport.VerifyTimeoutRequest) (*transport.VerifyTimeoutResponse, error)
	}
	core, ok := srv.(verifyCore)
	if !ok {
		return nil, unimplementedCore()
	}
	resp, err := core.ServeVerifyTimeout(ctx, transport.VerifyTimeoutRequestFromProto(&inner))
	if err != nil {
		return nil, mapCoreError(err)
	}
	return connect.NewResponse(transport.VerifyTimeoutResponseToProto(resp)), nil
}

func (h *SessionHandler) VerifyErrorMiss(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.VerifyErrorMissResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, srv, payload, err := h.openSignedOwner(ctx, req.Msg, "rpc_verify_error_miss")
	if err != nil {
		return nil, err
	}
	if err := requireOwner(srv, peer); err != nil {
		return nil, err
	}
	var inner rpcpb.VerifyErrorMissRequest
	if err := unmarshalPayload(payload, &inner); err != nil {
		return nil, err
	}
	type missCore interface {
		ServeVerifyErrorMiss(context.Context, transport.VerifyErrorMissRequest) (*transport.VerifyErrorMissResponse, error)
	}
	core, ok := srv.(missCore)
	if !ok {
		return nil, unimplementedCore()
	}
	resp, err := core.ServeVerifyErrorMiss(ctx, transport.VerifyErrorMissRequestFromProto(&inner))
	if err != nil {
		return nil, mapCoreError(err)
	}
	return connect.NewResponse(transport.VerifyErrorMissResponseToProto(resp)), nil
}

func (h *SessionHandler) ChallengeReceipt(ctx context.Context, req *connect.Request[rpcpb.SignedEnvelope]) (*connect.Response[rpcpb.ChallengeReceiptResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nil request"))
	}
	peer, escrow, err := requirePeer(ctx)
	if err != nil {
		return nil, err
	}
	if h.lookup == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session lookup not configured"))
	}
	env := req.Msg
	if env.GetEscrowId() != escrow {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("escrow mismatch"))
	}
	addr, vErr := transport.VerifyEnvelope(h.verifier, env, h.now())
	if vErr != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid envelope signature"))
	}
	if addr != peer {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("envelope signer does not match handshake"))
	}
	var inner rpcpb.ChallengeReceiptRequest
	if err := unmarshalPayload(env.GetPayload(), &inner); err != nil {
		return nil, err
	}
	jsonReq := transport.ChallengeReceiptRequestFromProto(&inner)
	diffs, err := transport.DiffsFromJSON(jsonReq.Diffs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	srv, err := h.lookup.SessionForStartProof(escrow, peer, diffs, jsonReq.ProtocolVersion)
	if err != nil {
		recordRPCSessionResolution(ctx, "rpc_challenge_receipt", escrow, err)
		return nil, mapAllowError(err)
	}
	if srv == nil {
		recordRPCSessionResolution(ctx, "rpc_challenge_receipt", escrow, storage.ErrSessionNotFound)
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
	}
	recordRPCSessionResolution(ctx, "rpc_challenge_receipt", escrow, nil)
	if !srv.AllowsSender(peer) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("peer is not a known participant"))
	}
	if err := requireOwnerOrGroup(srv, peer); err != nil {
		return nil, err
	}
	type challengeCore interface {
		ServeChallengeReceipt(context.Context, transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error)
	}
	core, ok := srv.(challengeCore)
	if !ok {
		return nil, unimplementedCore()
	}
	resp, err := core.ServeChallengeReceipt(ctx, jsonReq)
	if err != nil {
		return nil, mapCoreError(err)
	}
	return connect.NewResponse(transport.ChallengeReceiptResponseToProto(resp)), nil
}

func mapInferenceError(err error) error {
	if err == nil {
		return nil
	}
	var he *echo.HTTPError
	if errors.As(err, &he) {
		inner := errors.New(fmt.Sprint(he.Message))
		switch he.Code {
		case http.StatusBadRequest:
			return connect.NewError(connect.CodeInvalidArgument, inner)
		case http.StatusUnauthorized:
			return connect.NewError(connect.CodeUnauthenticated, inner)
		case http.StatusForbidden:
			return connect.NewError(connect.CodePermissionDenied, inner)
		case http.StatusRequestEntityTooLarge, http.StatusTooManyRequests:
			return connect.NewError(connect.CodeResourceExhausted, inner)
		case http.StatusServiceUnavailable:
			return withDevshardError(
				connect.NewError(connect.CodeUnavailable, inner),
				transport.DevshardErrorRequestsDisabled,
			)
		default:
			return connect.NewError(connect.CodeInternal, inner)
		}
	}
	return mapCoreError(err)
}

func mapCoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, devshard.ErrRequestsDisabled) {
		return withDevshardError(
			connect.NewError(connect.CodeUnavailable, errors.New(devshard.ErrRequestsDisabled.Error())),
			transport.DevshardErrorRequestsDisabled,
		)
	}
	if errors.Is(err, transport.ErrNoStorage) || errors.Is(err, transport.ErrHeightSyncSeedDisabled) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if errors.Is(err, transport.ErrInvalidRequesterSlot) ||
		errors.Is(err, transport.ErrGossipMissingStateSig) ||
		errors.Is(err, transport.ErrGossipInvalidSlot) ||
		errors.Is(err, transport.ErrGossipInvalidStateSig) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if errors.Is(err, transport.ErrRequesterSlotMismatch) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	if errors.Is(err, heightsync.ErrRepairUnknownTurn) {
		return connect.NewError(connect.CodeNotFound, errors.New("unknown turn"))
	}
	if errors.Is(err, heightsync.ErrRepairResponderBudget) {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("repair budget exhausted"))
	}
	if errors.Is(err, heightsync.ErrRepairVerify) ||
		errors.Is(err, heightsync.ErrRepairNoSig) ||
		errors.Is(err, heightsync.ErrRepairEmpty) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	if transport.IsClientRequest(err) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// MapSessionError is JSON sessionHTTPError on the Connect path.
func MapSessionError(err error) error {
	return mapAllowError(err)
}

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
	if errors.Is(err, bridge.ErrEscrowLookupLimited) {
		return withDevshardError(
			connect.NewError(connect.CodeResourceExhausted, errors.New("too many escrow lookups")),
			transport.DevshardErrorEscrowLookupLimited,
		)
	}
	if errors.Is(err, bridge.ErrEscrowNotFound) {
		return withDevshardError(
			connect.NewError(connect.CodeFailedPrecondition, errors.New("escrow is not open on this host")),
			transport.DevshardErrorEscrowNotFound,
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
	if errors.Is(err, types.ErrProtocolVersionMismatch) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("protocol version mismatch"))
	}
	if errors.Is(err, types.ErrStartProofMissing) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("gateway start proof required to open session"))
	}
	if errors.Is(err, types.ErrInvalidUserSig) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("invalid user signature"))
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

func recordRPCSessionResolution(ctx context.Context, route, escrowID string, err error) {
	status, reason := rpcResolutionStatus(err)
	observability.IncSessionResolution(route, status, reason)
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
	if errors.Is(err, bridge.ErrEscrowLookupLimited) {
		return observability.MetricStatusError, observability.ReasonRateLimited
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

package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"devshard"
	"devshard/heightsync"
	"devshard/logging"
	"devshard/observability"
)

// InferenceSink is the SSE byte destination. HTTP uses Echo's Response
// (gzip middleware). RPC wraps a gzip.Writer that emits ChatFrames.
type InferenceSink interface {
	http.ResponseWriter
	http.Flusher
}

// InferenceCall is one Chat / HandleInference execution.
type InferenceCall struct {
	SessionID     string
	Sender        string
	Body          []byte
	Source        string
	Evidence      *heightsync.RequestLegEvidence
	Sink          InferenceSink
	OnStreamStart func()
	Op            *observability.Operation
}

// ServeInference is the transport-neutral core behind POST .../chat/completions
// and SessionService.Chat. Callers enforce owner-only and supply a sink.
func (s *Server) ServeInference(ctx context.Context, call InferenceCall) error {
	if call.Sink == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "missing inference sink")
	}
	if !s.isOwner(call.Sender) {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonOwnerErr, observability.WhereTransportHandleInference,
			"HandleInference: restricted to escrow owner", echo.NewHTTPError(http.StatusForbidden, "restricted to escrow owner"))
	}

	unwrapped, err := UnwrapInferenceRequestBody(call.Body)
	if err != nil {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonParseErr, observability.WhereTransportHandleInference,
			"HandleInference: decode body", echo.NewHTTPError(http.StatusBadRequest, "decode body: "+err.Error()))
	}

	req, err := HostRequestFromJSON(unwrapped.Request)
	if err != nil {
		return observability.FailNoReceipt(ctx, s.host.EscrowID(),
			observability.ReasonDecodeErr, observability.WhereTransportHandleInference,
			"HandleInference: decode request", echo.NewHTTPError(http.StatusBadRequest, "decode request: "+err.Error()))
	}
	if call.Op != nil {
		if req.Payload != nil {
			observability.Request.SetModel(call.Op, req.Payload.Model)
		}
		observability.Request.SetNonce(call.Op, req.Nonce)
	}

	oracleHdr := s.latestOracleHeader(ctx)
	if s.pendingUntrustedBySession != nil {
		s.reconcilePendingUntrusted(call.SessionID, oracleHdr)
	}
	inboundVal := s.classifyInboundHeightSync(req.Nonce, unwrapped.HeightSync, oracleHdr)
	if inboundVal.Result == heightsync.ResultInvalidStaleOrigin {
		heightsync.IncStaleOriginRejected()
		logging.Warn("heightsync: invalid inbound anchor",
			heightsync.LogFieldSubsystem, "heightsync",
			heightsync.LogFieldDirection, "request",
			heightsync.LogFieldNonce, req.Nonce,
			heightsync.LogFieldPeerID, call.Sender,
			heightsync.LogFieldReason, inboundVal.Reason,
			heightsync.LogFieldClassification, string(inboundVal.Result),
		)
	}
	s.logInboundHeightSync(call.Sender, call.SessionID, req.Nonce, unwrapped.HeightSync, oracleHdr, inboundVal)
	s.recordInboundAnchorIfAnchor(call.Sender, unwrapped.HeightSync, call.Source, inboundVal)
	if inboundVal.Result == heightsync.ResultValidAnchor || inboundVal.Result == heightsync.ResultValidLazyAnchor {
		s.notePendingUntrustedInbound(call.SessionID, call.Sender, unwrapped.HeightSync, oracleHdr)
	}
	s.recordEnvelopeBinding(req, unwrapped.HeightSync, oracleHdr, call.Evidence)

	resp, err := s.host.HandleRequest(ctx, req)
	if err != nil {
		reason, where := observability.ErrorReason(err, observability.ReasonHandleRequestErr, observability.WhereTransportHandleInference)
		if errors.Is(err, devshard.ErrRequestsDisabled) {
			logging.Debug("HandleInference: devshard_requests_enabled=false", "subsystem", "server")
			call.Sink.Header().Set(HeaderDevshardError, DevshardErrorRequestsDisabled)
			return observability.FailNoReceipt(ctx, s.host.EscrowID(), reason, where,
				"HandleInference: requests disabled", echo.NewHTTPError(http.StatusServiceUnavailable, err.Error()))
		}
		return observability.FailNoReceipt(ctx, s.host.EscrowID(), reason, where,
			"HandleInference: handle request", echo.NewHTTPError(http.StatusInternalServerError, err.Error()).SetInternal(err))
	}
	s.recordForceRequestAnchorMissingIfApplicable(call.Sender, req.Nonce, unwrapped.HeightSync, call.Source)
	if call.Op != nil {
		observability.Request.SetInferenceID(call.Op, resp.InferenceID)
		observability.Request.SetInferenceResponse(call.Op, resp.Nonce, resp.ExecutionExpected, resp.CachedResponseBody != nil)
	}

	if err := s.waitInferenceResponseHold(ctx, req.Nonce); err != nil {
		logging.Debug("HandleInference: response hold ended without SSE",
			"subsystem", "transport", "nonce", req.Nonce, "error", err.Error())
		return err
	}

	if call.OnStreamStart != nil {
		call.OnStreamStart()
	}

	w := call.Sink
	receiptEvent := DevshardReceiptEvent{
		StateSig:          resp.StateSig,
		StateHash:         resp.StateHash,
		Nonce:             resp.Nonce,
		Receipt:           resp.Receipt,
		ConfirmedAt:       resp.ConfirmedAt,
		ObservedHeight:    resp.ObservedHeight,
		ObservedBlockHash: resp.ObservedBlockHash,
	}
	if s.receiptDelay > 0 {
		timer := time.NewTimer(s.receiptDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if resp.ExecutionJob != nil {
				s.host.ReleaseExecution(resp.InferenceID)
			}
			return nil
		case <-timer.C:
		}
	}
	receiptWrapper := map[string]interface{}{"devshard_receipt": receiptEvent}
	if s.heightSync != nil {
		schedK := s.heightSync.K()
		schedSlots := s.heightSync.SlotsNum()
		escrowH := s.host.HeightSyncEscrowHints(schedK, schedSlots)
		h := heightsync.DecideHints{
			Nonce:              req.Nonce,
			SessionStart:       req.Nonce == 1,
			ForceAnchor:        req.ForceHeightSyncAnchor && escrowH == nil,
			Escrow:             escrowH,
			OriginatorSenderID: s.host.Signer().Address(),
			Direction:          "response",
		}
		sec, dErr, oracleMiss := s.heightSync.Decide(ctx, h)
		if oracleMiss {
			heightsync.IncOracleFailure(s.host.Signer().Address())
		}
		if dErr != nil {
			logging.Debug("heightsync: outbound anchor error",
				heightsync.LogFieldSubsystem, "heightsync",
				heightsync.LogFieldNonce, req.Nonce,
				"error", dErr.Error())
			s.logOutboundHeightSync(nil, req.Nonce)
		} else if sec != nil {
			sec.Direction = "response"
			if s.attachResponseOriginSignature(sec, req.Nonce) {
				s.recordEnvelopeBindingResponse(req.Nonce, sec)
				receiptWrapper["height_sync"] = sec
				s.logOutboundHeightSync(sec, req.Nonce)
				s.recordOutboundAnchorIfAnchor(sec, call.Source)
			} else {
				s.logOutboundHeightSync(nil, req.Nonce)
			}
		} else {
			s.logOutboundHeightSync(nil, req.Nonce)
		}
	}
	if werr := writeSSEEvent(w, receiptWrapper); werr != nil {
		observability.RecordReceiptWriteFailure(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, observability.ReasonReceiptWriteErr, observability.WhereTransportWriteReceiptSSE)
		if resp.ExecutionJob != nil {
			s.host.ReleaseExecution(resp.InferenceID)
		}
		return nil
	}

	finishReason := observability.ReasonOK
	var finishFailureWhere observability.Where

	if resp.CachedResponseBody != nil && resp.ExecutionJob == nil {
		if werr := replaySSEBody(w, resp.CachedResponseBody); werr != nil {
			observability.RecordReceiptNoExecutionInterrupted(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, observability.ReasonCachedReplayErr, observability.WhereRuntimeWriteClientResponse)
			return nil
		}
	} else if resp.ExecutionJob != nil {
		resp.ExecutionJob.ResponseWriter = w
		execResult, execErr := s.host.RunExecution(ctx, resp.ExecutionJob)
		if execErr != nil {
			reason, where := observability.ErrorReason(execErr, observability.ReasonExecuteErr, observability.WhereHostExecute)
			if errors.Is(ctx.Err(), context.Canceled) {
				observability.RecordClientCancelledAfterReceipt(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, where)
				return nil
			}
			observability.RecordExecutionNoFinish(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, reason, where)
			logging.Error("deferred execution failed", "subsystem", "server", "error", execErr)
			return nil
		}
		if execResult != nil && execResult.PartialResponse {
			finishReason = observability.Reason(execResult.PartialResponseReason)
			if finishReason == "" {
				finishReason = observability.ReasonPartialResponseInterrupted
			}
			finishFailureWhere = observability.Where(execResult.PartialResponseWhere)
		}
	}

	mempoolTxs := s.host.MempoolTxs()
	mempoolBytes, _ := DevshardTxsToBytes(mempoolTxs)
	metaWrapper := map[string]interface{}{"devshard_meta": DevshardMetaEvent{Mempool: mempoolBytes}}
	_ = writeSSEEvent(w, metaWrapper)

	if s.gossip != nil && resp.StateSig != nil {
		go s.gossip.AfterRequest(context.Background(), resp.Nonce, resp.StateHash, resp.StateSig)
	}
	if s.gossip != nil && resp.StateSig == nil && len(resp.Mempool) > 0 {
		go s.gossip.BroadcastTxs(context.Background(), resp.Mempool)
	}

	switch {
	case resp.ExecutionExpected && resp.ExecutionJob != nil:
		observability.RecordFinishPublished(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, finishReason, finishFailureWhere)
	case resp.Receipt != nil:
		observability.RecordReceiptNoExecutionExpected(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, resp.ReceiptReason, observability.WhereHostSignReceipt)
	default:
		observability.RecordNoReceiptExpected(ctx, s.host.EscrowID(), resp.InferenceID, resp.Nonce, resp.ReceiptReason, observability.WhereHostSignReceipt)
	}

	return nil
}

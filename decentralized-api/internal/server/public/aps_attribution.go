package public

import (
	"context"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/agentenvelope"
	"decentralized-api/logging"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

// apsMsgTypeStartInference is the message type an authz grant must cover for a
// warm-key grantee to act for a developer principal.
const apsMsgTypeStartInference = "/inference.inference.MsgStartInference"

// validateAgentEnvelopeConfig guards against a misconfigured envelope layer. A
// verifier enabled with an empty chain_id would reject every envelope with a
// chain mismatch, so the layer is logged and disabled rather than started in
// that state. This surfaces the most common operator error at startup instead
// of at request time.
func validateAgentEnvelopeConfig(cfg apiconfig.AgentEnvelopeConfig) apiconfig.AgentEnvelopeConfig {
	if cfg.Enabled && cfg.ChainID == "" {
		logging.Error("agent_envelope is enabled but chain_id is empty; disabling the envelope layer until it is configured", types.Config)
		cfg.Enabled = false
	}
	return cfg
}

// apsAttributionSink adapts the agent-envelope attribution emitter to Gonka's
// structured logging. It is the one place that knows the log field layout.
type apsAttributionSink struct{}

func (apsAttributionSink) Emit(ev agentenvelope.AttributionEvent) {
	logging.Info("APS inference-start attribution", types.Inferences,
		"v", ev.Version,
		"inference_id", ev.InferenceID,
		"chain_id", ev.ChainID,
		"model", ev.Model,
		"agent_id", ev.AgentID,
		"principal_address", ev.PrincipalAddress,
		"beneficiary", ev.Beneficiary,
		"request_body_sha256", ev.RequestBodySHA256,
		"envelope_sha256", ev.EnvelopeSHA256,
		"passport_expires_at", ev.PassportExpiresAt,
		"verified_at", ev.VerifiedAt,
	)
}

// bindAgentEnvelope completes agent-envelope verification for a request that
// carried a verified envelope. It confirms the envelope principal matches the
// request requester, directly or through an authz grant on MsgStartInference,
// then marks the verified context request-bound. This is the handler-side
// half of the layering split: the middleware verified the crypto and scope,
// and only here, after the requester address has been parsed, can the
// principal be bound to the request. Attribution is emitted only for
// request-bound contexts.
func (s *Server) bindAgentEnvelope(reqCtx context.Context, apsCtx *agentenvelope.Context, requesterAddress string) error {
	principal := apsCtx.Envelope.PrincipalAddress
	if principal != requesterAddress {
		// The requester is not the principal. Accept only if the principal
		// granted the requester authz authority on MsgStartInference.
		// GetPubKeyForSigner returns the grantee key when an active grant
		// exists and an empty string otherwise; only grant existence is used
		// here. The requester's own signature on the request is validated by
		// Gonka's existing request-auth path, not by APS.
		granteePubkey, err := s.authzCache.GetPubKeyForSigner(reqCtx, principal, requesterAddress, apsMsgTypeStartInference)
		if err != nil {
			logging.Error("APS authz grantee resolution failed", types.Inferences,
				"principal", principal, "requester", requesterAddress, "error", err)
			return echo.NewHTTPError(agentenvelope.ErrChainQueryFailed.HTTPStatus(), agentenvelope.ErrChainQueryFailed.Code())
		}
		if granteePubkey == "" {
			return echo.NewHTTPError(agentenvelope.ErrPrincipalMismatch.HTTPStatus(), agentenvelope.ErrPrincipalMismatch.Code())
		}
	}
	apsCtx.RequestBound = true
	return nil
}

// emitAgentAttribution enqueues a structured attribution event for a verified,
// request-bound inference. The emitter is asynchronous and drops on
// backpressure, so this never blocks the request path.
func (s *Server) emitAgentAttribution(apsCtx *agentenvelope.Context, inferenceID, model string) {
	if s.apsEmitter == nil {
		return
	}
	env := apsCtx.Envelope
	s.apsEmitter.Enqueue(agentenvelope.AttributionEvent{
		Version:           "aps-attribution-v1",
		InferenceID:       inferenceID,
		ChainID:           env.ChainID,
		Model:             model,
		AgentID:           env.AgentID,
		PrincipalAddress:  env.PrincipalAddress,
		Beneficiary:       env.Beneficiary,
		RequestBodySHA256: apsCtx.RequestBodySHA256,
		EnvelopeSHA256:    apsCtx.EnvelopeSHA256,
		PassportExpiresAt: env.Scope.ExpiresAt,
		VerifiedAt:        apsCtx.VerifiedAt,
	})
}

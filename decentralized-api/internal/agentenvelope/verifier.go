package agentenvelope

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Config holds the verifier settings a node operator controls.
type Config struct {
	// ChainID is the Gonka chain ID this verifier accepts envelopes for.
	ChainID string
	// MaxTTL caps the lifetime (expires_at minus issued_at) of any envelope.
	MaxTTL time.Duration
}

// Verify runs the envelope verification flow over already-extracted request
// inputs and returns a verified Context on success.
//
// It covers steps 3 through 21 of the documented verification sequence:
// envelope parse, version, audience, chain_id, TTL, time window, principal and
// agent key decoding, address derivation, beneficiary validation, the ADR-036
// principal signature, the per-request agent signature, and the model scope
// check. Any failure stops the flow and returns the mapped EnvelopeError.
//
// Header presence and base64 decoding (steps 1 and 2), the bounded body read
// (steps 16 and 17), and model extraction are the middleware's responsibility.
// Verify is transport-agnostic so it can be unit tested without an HTTP stack.
//
// rawEnvelope is the base64-decoded X-Agent-Passport JSON. agentSigB64 is the
// X-Agent-Sig header value. body is the bounded request body. requestedModel
// is the model named in that body. now is the verification time.
//
// On success the returned Context has RequestBound set to false. The handler
// sets it to true only after it confirms the envelope principal matches the
// request's requester address, directly or through an authz grant.
func (cfg Config) Verify(rawEnvelope []byte, agentSigB64, method, path string, body []byte, requestedModel string, now time.Time) (*Context, error) {
	// Step 3: envelope parse.
	var env EnvelopeV1
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		return nil, ErrEnvelopeMalformed
	}

	// Step 4: schema version.
	if env.Version != SchemaVersion {
		return nil, ErrVersionUnknown
	}
	// Step 5: audience binding.
	if env.Audience != Audience {
		return nil, ErrAudienceMismatch
	}
	// Step 6: chain binding.
	if env.ChainID != cfg.ChainID {
		return nil, ErrChainIDMismatch
	}
	// Step 7: TTL bound. issued_at is the lifetime anchor.
	if env.Scope.ExpiresAt.Sub(env.IssuedAt) > cfg.MaxTTL {
		return nil, ErrTTLTooLong
	}
	// Step 8: time window.
	if env.Scope.NotBefore != nil && now.Before(*env.Scope.NotBefore) {
		return nil, ErrEnvelopeNotYetValid
	}
	if now.After(env.Scope.ExpiresAt) {
		return nil, ErrEnvelopeExpired
	}

	// Steps 9 and 10: principal pubkey type assertion and decode.
	principalPub, err := decodePrincipalPubkey(env.PrincipalPubkey)
	if err != nil {
		return nil, err
	}
	// Step 11: the embedded pubkey must derive to the stated address.
	if deriveAddress(principalPub) != env.PrincipalAddress {
		return nil, ErrPrincipalAddressMismatch
	}
	// Steps 12 and 13: agent pubkey type assertion and decode.
	agentPub, err := decodeAgentPubkey(env.AgentPubkey)
	if err != nil {
		return nil, err
	}
	// Step 14: beneficiary validation (reject on invalid, never transform).
	if err := validateBeneficiary(env.Beneficiary); err != nil {
		return nil, err
	}
	// Step 15: ADR-036 principal signature.
	if err := VerifyPrincipalSig(principalPub, env.PrincipalAddress, rawEnvelope, env.PrincipalSig); err != nil {
		return nil, err
	}
	// Steps 18 and 19: per-request agent signature.
	if err := VerifyAgentSig(agentPub, cfg.ChainID, method, path, rawEnvelope, body, agentSigB64); err != nil {
		return nil, err
	}
	// Step 20: model scope.
	if err := checkModelScope(env.Scope, requestedModel); err != nil {
		return nil, err
	}

	// Step 21: build the verified context. RequestBound stays false until the
	// handler confirms the principal/requester link.
	envHash := sha256.Sum256(rawEnvelope)
	bodyHash := sha256.Sum256(body)
	return &Context{
		Envelope:          &env,
		EnvelopeSHA256:    hex.EncodeToString(envHash[:]),
		RequestBodySHA256: hex.EncodeToString(bodyHash[:]),
		VerifiedAt:        now,
		RequestBound:      false,
	}, nil
}

// checkModelScope enforces the field-presence semantics of allowed_models from
// architectural principle P8:
//   - field omitted (nil): all models allowed
//   - field present and empty: deny all
//   - field present and non-empty: only the listed models allowed
func checkModelScope(scope Scope, requestedModel string) error {
	if scope.AllowedModels == nil {
		return nil
	}
	models := *scope.AllowedModels
	if len(models) == 0 {
		return ErrAllowedModelsEmpty
	}
	for _, m := range models {
		if m == requestedModel {
			return nil
		}
	}
	return ErrModelNotAllowed
}

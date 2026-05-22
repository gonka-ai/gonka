package agentenvelope

import "net/http"

// EnvelopeError is a verification failure with a stable machine-readable code
// and the HTTP status the middleware should return.
//
// Status mapping follows architectural principle P14:
//   - 400: the request is structurally malformed (bad base64, bad JSON, a
//     header pair where one of the two headers is missing).
//   - 401: a credential is wrong. The client must rebuild the envelope. This
//     covers bad signatures, wrong chain_id, wrong audience, an unknown
//     version, an address that does not derive from the embedded pubkey, a
//     wrong pubkey type, and an invalid beneficiary.
//   - 403: the credential is valid but the request is not permitted. This
//     covers expiry, not-yet-valid, an over-long TTL, a model outside scope,
//     an explicit deny-all model list, and a principal that does not match
//     the requester.
//   - 413: the request body exceeds the configured maximum.
//   - 500: an internal error, including a chain query failure during authz
//     grantee resolution.
type EnvelopeError struct {
	code   string
	status int
	msg    string
}

// Error implements the error interface.
func (e *EnvelopeError) Error() string { return e.msg }

// Code returns the stable machine-readable error code.
func (e *EnvelopeError) Code() string { return e.code }

// HTTPStatus returns the HTTP status code the middleware should return.
func (e *EnvelopeError) HTTPStatus() int { return e.status }

func newErr(code string, status int, msg string) *EnvelopeError {
	return &EnvelopeError{code: code, status: status, msg: msg}
}

// Named verification errors. The verifier returns these so the middleware can
// map a single failure to a stable code and the correct HTTP status.
var (
	// 400 structural.
	ErrSigHeaderMismatch = newErr("sig_header_mismatch", http.StatusBadRequest,
		"agentenvelope: one of X-Agent-Passport or X-Agent-Sig is present without the other")
	ErrHeaderMalformed = newErr("header_malformed", http.StatusBadRequest,
		"agentenvelope: a passport header is not valid base64")
	ErrEnvelopeMalformed = newErr("envelope_malformed", http.StatusBadRequest,
		"agentenvelope: the envelope is not valid JSON or is missing a required field")

	// 401 credential wrong.
	ErrVersionUnknown = newErr("version_unknown", http.StatusUnauthorized,
		"agentenvelope: unknown envelope schema version")
	ErrAudienceMismatch = newErr("audience_mismatch", http.StatusUnauthorized,
		"agentenvelope: envelope audience does not match this verifier")
	ErrChainIDMismatch = newErr("chain_id_mismatch", http.StatusUnauthorized,
		"agentenvelope: envelope chain_id does not match this verifier")
	ErrPrincipalPubkeyTypeInvalid = newErr("principal_pubkey_type_invalid", http.StatusUnauthorized,
		"agentenvelope: principal pubkey type is not secp256k1")
	ErrPrincipalPubkeyMalformed = newErr("principal_pubkey_malformed", http.StatusUnauthorized,
		"agentenvelope: principal pubkey is not a valid compressed secp256k1 key")
	ErrPrincipalMultisigUnsupported = newErr("principal_multisig_unsupported", http.StatusUnauthorized,
		"agentenvelope: multi-signature principal keys are not supported in v1")
	ErrPrincipalAddressMismatch = newErr("principal_address_mismatch", http.StatusUnauthorized,
		"agentenvelope: principal address does not derive from the embedded pubkey")
	ErrAgentPubkeyTypeInvalid = newErr("agent_pubkey_type_invalid", http.StatusUnauthorized,
		"agentenvelope: agent pubkey type is not ed25519")
	ErrAgentPubkeyMalformed = newErr("agent_pubkey_malformed", http.StatusUnauthorized,
		"agentenvelope: agent pubkey is not a valid 32-byte ed25519 key")
	ErrBeneficiaryInvalid = newErr("beneficiary_invalid", http.StatusUnauthorized,
		"agentenvelope: beneficiary contains control characters, invalid UTF-8, or exceeds the byte limit")
	ErrPrincipalSigInvalid = newErr("principal_sig_invalid", http.StatusUnauthorized,
		"agentenvelope: principal signature verification failed")
	ErrAgentSigInvalid = newErr("agent_sig_invalid", http.StatusUnauthorized,
		"agentenvelope: agent signature verification failed")

	// 403 not permitted.
	ErrTTLTooLong = newErr("ttl_too_long", http.StatusForbidden,
		"agentenvelope: envelope lifetime exceeds the configured maximum TTL")
	ErrEnvelopeNotYetValid = newErr("envelope_not_yet_valid", http.StatusForbidden,
		"agentenvelope: envelope not_before is in the future")
	ErrEnvelopeExpired = newErr("envelope_expired", http.StatusForbidden,
		"agentenvelope: envelope has expired")
	ErrAllowedModelsEmpty = newErr("allowed_models_empty", http.StatusForbidden,
		"agentenvelope: envelope scope allows zero models")
	ErrModelNotAllowed = newErr("model_not_allowed", http.StatusForbidden,
		"agentenvelope: requested model is not in the envelope scope")
	ErrPrincipalMismatch = newErr("principal_mismatch", http.StatusForbidden,
		"agentenvelope: envelope principal does not match the requester and no authz grant resolves it")

	// 413 body bound.
	ErrBodyTooLarge = newErr("body_too_large", http.StatusRequestEntityTooLarge,
		"agentenvelope: request body exceeds the configured maximum size")

	// 500 internal.
	ErrChainQueryFailed = newErr("chain_query_failed", http.StatusInternalServerError,
		"agentenvelope: authz grantee resolution chain query failed")
)

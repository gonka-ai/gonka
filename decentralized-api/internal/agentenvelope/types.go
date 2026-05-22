// Package agentenvelope verifies an optional signed agent request envelope on
// Gonka inference requests.
//
// The envelope is opt-in metadata. When present it carries a cryptographic
// agent identity, a request-layer delegation scope, and a principal-attested
// beneficiary reference. It composes with Gonka's existing Developer, Transfer
// Agent, and Executor signature chain without replacing any part of it. When
// the envelope headers are absent, request behavior is unchanged.
//
// The wire format is APS-compatible (Agent Passport System). Nothing in this
// package requires Gonka to adopt APS as a network-wide standard; the verifier
// is self-contained and the schema is versioned.
package agentenvelope

import "time"

// SchemaVersion is the only envelope schema version this verifier accepts.
const SchemaVersion = "aps-agent-envelope-v1"

// Audience is the required protocol-surface binding value. It scopes a
// passport to the Gonka inference surface so a passport signed for this
// surface cannot be replayed against a different protocol on the same chain.
const Audience = "gonka"

// Key type tags.
const (
	KeyTypeEd25519   = "ed25519"
	KeyTypeSecp256k1 = "secp256k1"
)

// MaxBeneficiaryBytes bounds the principal-attested beneficiary string.
const MaxBeneficiaryBytes = 255

// TypedKey is a curve-tagged public key. PublicKey is the standard-base64
// encoding of the raw key bytes (32 bytes for ed25519, 33 bytes compressed
// for secp256k1).
type TypedKey struct {
	Type      string `json:"type"`
	PublicKey string `json:"public_key"`
}

// Scope carries the request-layer delegation limits the principal attests.
//
// AllowedModels uses field-presence semantics, per architectural principle P8:
//   - nil pointer (field omitted): all models allowed
//   - non-nil pointer to an empty slice (field present as []): deny all
//   - non-nil pointer to a non-empty slice: only the listed models allowed
type Scope struct {
	AllowedModels *[]string  `json:"allowed_models,omitempty"`
	ExpiresAt     time.Time  `json:"expires_at"`
	NotBefore     *time.Time `json:"not_before,omitempty"`
}

// EnvelopeV1 is the aps-agent-envelope-v1 schema.
//
// PrincipalSig is the standard-base64 ADR-036 signature produced by the
// principal's secp256k1 key over the JCS-canonical envelope with the
// principal_sig field set to the empty string. The field carries no omitempty
// tag: it is always present on the wire, and empty during canonicalization.
type EnvelopeV1 struct {
	Version          string    `json:"v"`
	Audience         string    `json:"audience"`
	ChainID          string    `json:"chain_id"`
	AgentID          string    `json:"agent_id"`
	AgentPubkey      TypedKey  `json:"agent_pubkey"`
	PrincipalAddress string    `json:"principal_address"`
	PrincipalPubkey  TypedKey  `json:"principal_pubkey"`
	Scope            Scope     `json:"scope"`
	Beneficiary      string    `json:"beneficiary,omitempty"`
	IssuedAt         time.Time `json:"issued_at"`
	PrincipalSig     string    `json:"principal_sig"`
}

// AttributionEvent is the structured record emitted after a verified,
// request-bound inference is submitted to chain. It is observability data in
// v1, not an on-chain record.
type AttributionEvent struct {
	Version           string    `json:"v"`
	InferenceID       string    `json:"inference_id"`
	ChainID           string    `json:"chain_id"`
	Model             string    `json:"model"`
	AgentID           string    `json:"agent_id"`
	PrincipalAddress  string    `json:"principal_address"`
	Beneficiary       string    `json:"beneficiary,omitempty"`
	RequestBodySHA256 string    `json:"request_body_sha256"`
	EnvelopeSHA256    string    `json:"envelope_sha256"`
	PassportExpiresAt time.Time `json:"passport_expires_at"`
	VerifiedAt        time.Time `json:"verified_at"`
}

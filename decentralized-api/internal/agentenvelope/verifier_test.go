package agentenvelope

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"decentralized-api/internal/agentenvelope/jcs"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

const (
	testChainID = "gonka-test"
	testMethod  = "POST"
	testPath    = "/v1/chat/completions"
	testModel   = "Qwen/QwQ-32B"
)

// harness holds fresh keys and request material for one verifier test.
type harness struct {
	t             *testing.T
	cfg           Config
	principalPriv *secp256k1.PrivKey
	principalPub  *secp256k1.PubKey
	agentPriv     ed25519.PrivateKey
	agentPub      ed25519.PublicKey
	now           time.Time
	body          []byte
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pp := secp256k1.GenPrivKey()
	apub, apriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{
		t:             t,
		cfg:           Config{ChainID: testChainID, MaxTTL: time.Hour},
		principalPriv: pp,
		principalPub:  pp.PubKey().(*secp256k1.PubKey),
		agentPriv:     apriv,
		agentPub:      apub,
		now:           time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC),
		body:          []byte(`{"model":"` + testModel + `","messages":[]}`),
	}
}

// validEnvelope returns a default well-formed envelope for the harness keys.
func (h *harness) validEnvelope() *EnvelopeV1 {
	models := []string{testModel}
	return &EnvelopeV1{
		Version:          SchemaVersion,
		Audience:         Audience,
		ChainID:          testChainID,
		AgentID:          "urn:aps:agent:test",
		AgentPubkey:      TypedKey{Type: KeyTypeEd25519, PublicKey: base64.StdEncoding.EncodeToString(h.agentPub)},
		PrincipalAddress: deriveAddress(h.principalPub),
		PrincipalPubkey:  TypedKey{Type: KeyTypeSecp256k1, PublicKey: base64.StdEncoding.EncodeToString(h.principalPub.Key)},
		Scope: Scope{
			AllowedModels: &models,
			ExpiresAt:     h.now.Add(30 * time.Minute),
		},
		Beneficiary: "urn:customer:acme-corp",
		IssuedAt:    h.now,
	}
}

// sign serializes env, produces the ADR-036 principal signature with the
// harness principal key and the per-request agent signature with the harness
// agent key, and returns the wire envelope and the agent signature header.
func (h *harness) sign(env *EnvelopeV1) (rawEnvelope []byte, agentSigB64 string) {
	h.t.Helper()
	env.PrincipalSig = ""
	unsigned, err := json.Marshal(env)
	if err != nil {
		h.t.Fatal(err)
	}
	canon, err := principalCanonicalJSON(unsigned)
	if err != nil {
		h.t.Fatal(err)
	}
	signBytes, err := adr036SignBytes(env.PrincipalAddress, canon)
	if err != nil {
		h.t.Fatal(err)
	}
	psig, err := h.principalPriv.Sign(signBytes)
	if err != nil {
		h.t.Fatal(err)
	}
	env.PrincipalSig = base64.StdEncoding.EncodeToString(psig)
	rawEnvelope, err = json.Marshal(env)
	if err != nil {
		h.t.Fatal(err)
	}
	envJCS, err := jcs.Canonicalize(rawEnvelope)
	if err != nil {
		h.t.Fatal(err)
	}
	payload := AgentSigPayload(testChainID, testMethod, testPath, envJCS, h.body)
	return rawEnvelope, base64.StdEncoding.EncodeToString(ed25519.Sign(h.agentPriv, payload))
}

// verify runs the harness default request against the verifier.
func (h *harness) verify(raw []byte, agentSig string) (*Context, error) {
	return h.cfg.Verify(raw, agentSig, testMethod, testPath, h.body, testModel, h.now)
}

func TestVerify_HappyPath(t *testing.T) {
	h := newHarness(t)
	raw, sig := h.sign(h.validEnvelope())
	ctx, err := h.verify(raw, sig)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if ctx == nil || ctx.Envelope == nil {
		t.Fatal("verified context or envelope is nil")
	}
	if ctx.RequestBound {
		t.Error("RequestBound must be false after the middleware-stage verify")
	}
	if ctx.EnvelopeSHA256 == "" || ctx.RequestBodySHA256 == "" {
		t.Error("verified context is missing content hashes")
	}
}

func TestVerify_AllowedModelsOmittedAllowsAll(t *testing.T) {
	h := newHarness(t)
	env := h.validEnvelope()
	env.Scope.AllowedModels = nil
	raw, sig := h.sign(env)
	if _, err := h.verify(raw, sig); err != nil {
		t.Errorf("omitted allowed_models should allow all models, got %v", err)
	}
}

// TestVerify_FieldErrors mutates one envelope field before signing and asserts
// the verifier rejects it with the expected error. Each mutated field is
// caught by its own step, ahead of the signature checks.
func TestVerify_FieldErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *EnvelopeV1, h *harness)
		want   *EnvelopeError
	}{
		{"version", func(e *EnvelopeV1, h *harness) { e.Version = "aps-agent-envelope-v2" }, ErrVersionUnknown},
		{"missing agent_id", func(e *EnvelopeV1, h *harness) { e.AgentID = "" }, ErrEnvelopeMalformed},
		{"zero issued_at", func(e *EnvelopeV1, h *harness) { e.IssuedAt = time.Time{} }, ErrEnvelopeMalformed},
		{"future issued_at", func(e *EnvelopeV1, h *harness) {
			e.IssuedAt = h.now.Add(time.Hour)
			e.Scope.ExpiresAt = h.now.Add(90 * time.Minute)
		}, ErrEnvelopeNotYetValid},
		{"audience", func(e *EnvelopeV1, h *harness) { e.Audience = "not-gonka" }, ErrAudienceMismatch},
		{"chain id", func(e *EnvelopeV1, h *harness) { e.ChainID = "other-chain" }, ErrChainIDMismatch},
		{"ttl too long", func(e *EnvelopeV1, h *harness) { e.Scope.ExpiresAt = h.now.Add(2 * time.Hour) }, ErrTTLTooLong},
		{"not yet valid", func(e *EnvelopeV1, h *harness) {
			nb := h.now.Add(time.Hour)
			e.Scope.NotBefore = &nb
		}, ErrEnvelopeNotYetValid},
		{"expired", func(e *EnvelopeV1, h *harness) {
			e.IssuedAt = h.now.Add(-10 * time.Minute)
			e.Scope.ExpiresAt = h.now.Add(-time.Minute)
		}, ErrEnvelopeExpired},
		{"principal pubkey type", func(e *EnvelopeV1, h *harness) { e.PrincipalPubkey.Type = KeyTypeEd25519 }, ErrPrincipalPubkeyTypeInvalid},
		{"principal pubkey multisig", func(e *EnvelopeV1, h *harness) {
			e.PrincipalPubkey.Type = "tendermint/PubKeyMultisigThreshold"
		}, ErrPrincipalMultisigUnsupported},
		{"principal pubkey malformed", func(e *EnvelopeV1, h *harness) { e.PrincipalPubkey.PublicKey = "not!base64" }, ErrPrincipalPubkeyMalformed},
		{"principal address mismatch", func(e *EnvelopeV1, h *harness) {
			e.PrincipalAddress = deriveAddress(secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey))
		}, ErrPrincipalAddressMismatch},
		{"agent pubkey type", func(e *EnvelopeV1, h *harness) { e.AgentPubkey.Type = KeyTypeSecp256k1 }, ErrAgentPubkeyTypeInvalid},
		{"agent pubkey malformed", func(e *EnvelopeV1, h *harness) { e.AgentPubkey.PublicKey = "not!base64" }, ErrAgentPubkeyMalformed},
		{"beneficiary invalid", func(e *EnvelopeV1, h *harness) {
			e.Beneficiary = "bad" + string(rune(0x01)) + "control"
		}, ErrBeneficiaryInvalid},
		{"allowed models empty", func(e *EnvelopeV1, h *harness) {
			empty := []string{}
			e.Scope.AllowedModels = &empty
		}, ErrAllowedModelsEmpty},
		{"model not allowed", func(e *EnvelopeV1, h *harness) {
			m := []string{"some-other-model"}
			e.Scope.AllowedModels = &m
		}, ErrModelNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			env := h.validEnvelope()
			tc.mutate(env, h)
			raw, sig := h.sign(env)
			if _, err := h.verify(raw, sig); err != tc.want {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestVerify_EnvelopeMalformed(t *testing.T) {
	h := newHarness(t)
	if _, err := h.verify([]byte("{not valid json"), ""); err != ErrEnvelopeMalformed {
		t.Errorf("got %v, want ErrEnvelopeMalformed", err)
	}
}

func TestVerify_PrincipalSigInvalid(t *testing.T) {
	h := newHarness(t)
	raw, sig := h.sign(h.validEnvelope())

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	// Replace principal_sig with a well-formed but wrong signature.
	m["principal_sig"] = base64.StdEncoding.EncodeToString(make([]byte, 64))
	tampered, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.verify(tampered, sig); err != ErrPrincipalSigInvalid {
		t.Errorf("got %v, want ErrPrincipalSigInvalid", err)
	}
}

func TestVerify_AgentSigInvalid(t *testing.T) {
	h := newHarness(t)
	raw, _ := h.sign(h.validEnvelope())
	wrongSig := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if _, err := h.verify(raw, wrongSig); err != ErrAgentSigInvalid {
		t.Errorf("got %v, want ErrAgentSigInvalid", err)
	}
}

func TestVerify_TamperedBodyFailsAgentSig(t *testing.T) {
	h := newHarness(t)
	raw, sig := h.sign(h.validEnvelope())
	// A body different from the one the agent signed must fail step 19.
	_, err := h.cfg.Verify(raw, sig, testMethod, testPath, []byte(`{"model":"`+testModel+`","messages":[{"role":"user"}]}`), testModel, h.now)
	if err != ErrAgentSigInvalid {
		t.Errorf("got %v, want ErrAgentSigInvalid", err)
	}
}

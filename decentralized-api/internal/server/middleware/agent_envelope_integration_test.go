package middleware

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"decentralized-api/internal/agentenvelope"
	"decentralized-api/internal/agentenvelope/jcs"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/labstack/echo/v4"
)

const (
	itChainID = "gonka-integration"
	itModel   = "Qwen/QwQ-32B"
	itPath    = "/v1/chat/completions"
)

// signedRequest is a fully signed agent request: the two header values and
// the request body the agent signature was produced over.
type signedRequest struct {
	passportHeader string
	agentSigHeader string
	body           []byte
}

// buildSignedRequest produces a real, fully signed agent request using only
// the exported agentenvelope API, the way a client SDK would. The optional
// mutate hook adjusts the envelope before it is signed.
func buildSignedRequest(t testing.TB, mutate func(*agentenvelope.EnvelopeV1)) signedRequest {
	t.Helper()
	principalPriv := secp256k1.GenPrivKey()
	principalPub := principalPriv.PubKey().(*secp256k1.PubKey)
	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	models := []string{itModel}
	env := &agentenvelope.EnvelopeV1{
		Version:          agentenvelope.SchemaVersion,
		Audience:         agentenvelope.Audience,
		ChainID:          itChainID,
		AgentID:          "urn:aps:agent:integration",
		AgentPubkey:      agentenvelope.TypedKey{Type: agentenvelope.KeyTypeEd25519, PublicKey: base64.StdEncoding.EncodeToString(agentPub)},
		PrincipalAddress: sdk.AccAddress(principalPub.Address()).String(),
		PrincipalPubkey:  agentenvelope.TypedKey{Type: agentenvelope.KeyTypeSecp256k1, PublicKey: base64.StdEncoding.EncodeToString(principalPub.Key)},
		Scope: agentenvelope.Scope{
			AllowedModels: &models,
			ExpiresAt:     now.Add(30 * time.Minute),
		},
		Beneficiary: "urn:customer:integration",
		IssuedAt:    now,
	}
	if mutate != nil {
		mutate(env)
	}
	body := []byte(`{"model":"` + itModel + `","messages":[]}`)

	// Principal signature: ADR-036 over the envelope with principal_sig empty.
	env.PrincipalSig = ""
	unsigned, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	signBytes, err := agentenvelope.PrincipalSignBytes(env.PrincipalAddress, unsigned)
	if err != nil {
		t.Fatal(err)
	}
	psig, err := principalPriv.Sign(signBytes)
	if err != nil {
		t.Fatal(err)
	}
	env.PrincipalSig = base64.StdEncoding.EncodeToString(psig)

	rawEnvelope, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// Agent signature: Ed25519 over the length-prefixed payload binding the
	// chain, method, path, full canonical envelope, and body.
	envJCS, err := jcs.Canonicalize(rawEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	payload := agentenvelope.AgentSigPayload(env.ChainID, http.MethodPost, itPath, envJCS, body)
	agentSig := ed25519.Sign(agentPriv, payload)

	return signedRequest{
		passportHeader: base64.StdEncoding.EncodeToString(rawEnvelope),
		agentSigHeader: base64.StdEncoding.EncodeToString(agentSig),
		body:           body,
	}
}

// itServer builds an Echo instance with the agent-envelope middleware mounted
// on a recording handler. The returned function reports the verified context
// the handler observed, or nil.
func itServer(t testing.TB, maxBody int64) (*echo.Echo, func() *agentenvelope.Context) {
	t.Helper()
	e := echo.New()
	var captured *agentenvelope.Context
	cfg := agentenvelope.Config{ChainID: itChainID, MaxTTL: time.Hour}
	mw := AgentEnvelopeMiddleware(true, cfg, maxBody)
	e.POST(itPath, func(c echo.Context) error {
		captured = agentenvelope.FromEchoContext(c)
		if _, err := io.ReadAll(c.Request().Body); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	}, mw)
	return e, func() *agentenvelope.Context { return captured }
}

func sendIT(e *echo.Echo, sr signedRequest, overrideBody []byte) *httptest.ResponseRecorder {
	body := sr.body
	if overrideBody != nil {
		body = overrideBody
	}
	req := httptest.NewRequest(http.MethodPost, itPath, bytes.NewReader(body))
	if sr.passportHeader != "" {
		req.Header.Set(headerPassport, sr.passportHeader)
	}
	if sr.agentSigHeader != "" {
		req.Header.Set(headerAgentSig, sr.agentSigHeader)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestIntegration_HappyPath(t *testing.T) {
	e, captured := itServer(t, 10<<20)
	rec := sendIT(e, buildSignedRequest(t, nil), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("happy path: code %d, body %s", rec.Code, rec.Body.String())
	}
	apsCtx := captured()
	if apsCtx == nil || apsCtx.Envelope == nil {
		t.Fatal("verified context not attached for the handler")
	}
	if apsCtx.RequestBound {
		t.Error("RequestBound must be false after the middleware stage")
	}
	if apsCtx.Envelope.AgentID != "urn:aps:agent:integration" {
		t.Errorf("unexpected agent id %q", apsCtx.Envelope.AgentID)
	}
}

func TestIntegration_EnvelopeTamperFails(t *testing.T) {
	e, _ := itServer(t, 10<<20)
	sr := buildSignedRequest(t, nil)

	// Tamper a principal-signed field after signing.
	raw, err := base64.StdEncoding.DecodeString(sr.passportHeader)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["agent_id"] = "urn:aps:agent:attacker"
	tampered, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	sr.passportHeader = base64.StdEncoding.EncodeToString(tampered)

	if rec := sendIT(e, sr, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered envelope: code %d, want 401", rec.Code)
	}
}

func TestIntegration_BodyTamperFails(t *testing.T) {
	e, _ := itServer(t, 10<<20)
	sr := buildSignedRequest(t, nil)
	other := []byte(`{"model":"` + itModel + `","messages":[{"role":"user","content":"x"}]}`)
	if rec := sendIT(e, sr, other); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered body: code %d, want 401", rec.Code)
	}
}

func TestIntegration_WrongChainFails(t *testing.T) {
	e, _ := itServer(t, 10<<20)
	sr := buildSignedRequest(t, func(env *agentenvelope.EnvelopeV1) {
		env.ChainID = "some-other-chain"
	})
	if rec := sendIT(e, sr, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong chain id: code %d, want 401", rec.Code)
	}
}

func TestIntegration_ModelNotAllowedFails(t *testing.T) {
	e, _ := itServer(t, 10<<20)
	sr := buildSignedRequest(t, func(env *agentenvelope.EnvelopeV1) {
		models := []string{"some-other-model"}
		env.Scope.AllowedModels = &models
	})
	if rec := sendIT(e, sr, nil); rec.Code != http.StatusForbidden {
		t.Errorf("model not allowed: code %d, want 403", rec.Code)
	}
}

func TestIntegration_BodyTooLarge(t *testing.T) {
	e, _ := itServer(t, 1<<20) // 1 MiB cap
	sr := buildSignedRequest(t, nil)
	oversized := bytes.Repeat([]byte("a"), 2<<20)
	if rec := sendIT(e, sr, oversized); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: code %d, want 413", rec.Code)
	}
}

func BenchmarkMiddleware_AbsentHeader(b *testing.B) {
	e, _ := itServer(b, 10<<20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, itPath, strings.NewReader("{}"))
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkMiddleware_ValidEnvelope(b *testing.B) {
	e, _ := itServer(b, 10<<20)
	sr := buildSignedRequest(b, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sendIT(e, sr, nil)
	}
}

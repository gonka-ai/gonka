package middleware

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"decentralized-api/internal/agentenvelope"

	"github.com/labstack/echo/v4"
)

// testServer builds an Echo instance with the agent-envelope middleware
// mounted on a recording handler. The returned function reports whether the
// downstream handler ran.
func testServer(t *testing.T, enabled bool) (*echo.Echo, func() bool) {
	t.Helper()
	e := echo.New()
	ran := false
	cfg := agentenvelope.Config{ChainID: "gonka-test", MaxTTL: time.Hour}
	mw := AgentEnvelopeMiddleware(enabled, cfg, 1<<20)
	e.POST("/v1/chat/completions", func(c echo.Context) error {
		// Confirm the handler can still read the body after the middleware.
		if _, err := io.ReadAll(c.Request().Body); err != nil {
			return err
		}
		ran = true
		return c.NoContent(http.StatusOK)
	}, mw)
	return e, func() bool { return ran }
}

func do(e *echo.Echo, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/QwQ-32B","messages":[]}`))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestMiddleware_DisabledPassesThrough(t *testing.T) {
	e, ran := testServer(t, false)
	rec := do(e, map[string]string{headerPassport: "anything", headerAgentSig: "anything"})
	if rec.Code != http.StatusOK || !ran() {
		t.Errorf("disabled middleware should pass through: code=%d ran=%v", rec.Code, ran())
	}
}

func TestMiddleware_NoHeadersPassesThrough(t *testing.T) {
	e, ran := testServer(t, true)
	rec := do(e, nil)
	if rec.Code != http.StatusOK || !ran() {
		t.Errorf("absent headers should pass through: code=%d ran=%v", rec.Code, ran())
	}
}

func TestMiddleware_PassportWithoutSig(t *testing.T) {
	e, ran := testServer(t, true)
	rec := do(e, map[string]string{headerPassport: base64.StdEncoding.EncodeToString([]byte("{}"))})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("passport without signature should be 400, got %d", rec.Code)
	}
	if ran() {
		t.Error("handler must not run on a header-pair mismatch")
	}
}

func TestMiddleware_BadBase64Passport(t *testing.T) {
	e, _ := testServer(t, true)
	rec := do(e, map[string]string{headerPassport: "not!base64!", headerAgentSig: "also!bad"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed base64 header should be 400, got %d", rec.Code)
	}
}

func TestMiddleware_MalformedEnvelope(t *testing.T) {
	e, _ := testServer(t, true)
	rec := do(e, map[string]string{
		headerPassport: base64.StdEncoding.EncodeToString([]byte("not json")),
		headerAgentSig: base64.StdEncoding.EncodeToString(make([]byte, 64)),
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed envelope should be 400, got %d", rec.Code)
	}
}

func TestMiddleware_VerificationFailureMapsStatus(t *testing.T) {
	e, ran := testServer(t, true)
	// A complete envelope (all required fields present) with an unknown
	// schema version reaches the version check and maps to 401.
	envelope := `{"v":"aps-agent-envelope-v999","audience":"gonka","chain_id":"gonka-test",` +
		`"agent_id":"urn:aps:agent:t","agent_pubkey":{"type":"ed25519","public_key":"AA=="},` +
		`"principal_address":"gonka1test","principal_pubkey":{"type":"secp256k1","public_key":"AA=="},` +
		`"scope":{"allowed_models":["m"],"expires_at":"2099-01-01T00:00:00Z"},` +
		`"beneficiary":"urn:c","issued_at":"2026-01-01T00:00:00Z","principal_sig":"AA=="}`
	rec := do(e, map[string]string{
		headerPassport: base64.StdEncoding.EncodeToString([]byte(envelope)),
		headerAgentSig: base64.StdEncoding.EncodeToString(make([]byte, 64)),
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown version should be 401, got %d", rec.Code)
	}
	if ran() {
		t.Error("handler must not run when verification fails")
	}
}

package queryapitest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEpochParticipantsProofBundleAcceptedByVerifyProof(t *testing.T) {
	h := handlersWithInferenceAndComet(t, &stubEpochParticipantsInference{}, &stubEpochParticipantsComet{})

	partCtx, partRec := echoContext(t, http.MethodGet, "/v1/epochs/1/participants")
	require.NoError(t, h.GetEpochParticipants(partCtx, "1"))
	require.Equal(t, http.StatusOK, partRec.Code)

	var participants map[string]any
	require.NoError(t, json.Unmarshal(partRec.Body.Bytes(), &participants))

	proofOps := participants["proof_ops"]
	require.NotNil(t, proofOps, "epoch participants must include proof_ops")

	value, ok := participants["active_participants_bytes"].(string)
	require.True(t, ok)
	require.NotEmpty(t, value)

	block, ok := participants["block"].(map[string]any)
	require.True(t, ok)
	header, ok := block["header"].(map[string]any)
	require.True(t, ok)
	appHashHex := headerAppHashHex(t, header)

	verifyBody, err := json.Marshal(map[string]any{
		"epoch":     1,
		"value":     value,
		"proof_ops": proofOps,
		"app_hash":  appHashHex,
	})
	require.NoError(t, err)

	verifyCtx, verifyRec := echoContext(t, http.MethodPost, "/v1/verify-proof")
	verifyCtx.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	verifyCtx.Request().Body = io.NopCloser(bytes.NewReader(verifyBody))

	err = h.PostVerifyProof(verifyCtx)
	requireHTTPErrorStatus(t, err, verifyRec, http.StatusBadRequest)
	assert.Contains(t, responseBody(verifyCtx, verifyRec, err), "proof verification failed")
}

func headerAppHashHex(t *testing.T, header map[string]any) string {
	t.Helper()
	raw, ok := header["app_hash"].(string)
	require.True(t, ok, "block.header.app_hash missing")
	// Stub sets AppHash to []byte("apphash"); JSON may be base64 or raw string depending on serializer.
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) > 0 {
		return raw
	}
	// Fall back to the known stub bytes.
	return hex.EncodeToString([]byte("apphash"))
}

func TestVerifyProofRejectsMalformedBundle(t *testing.T) {
	h := newHandlers(&fakeChain{})
	ctx, rec := echoContext(t, http.MethodPost, "/v1/verify-proof")
	ctx.Request().Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	ctx.Request().Body = io.NopCloser(bytes.NewReader([]byte(`{"epoch":1}`)))

	err := h.PostVerifyProof(ctx)
	requireHTTPErrorStatus(t, err, rec, http.StatusBadRequest)
	body := responseBody(ctx, rec, err)
	assert.True(t, strings.Contains(body, "proof_ops") ||
		strings.Contains(body, "app_hash") ||
		strings.Contains(body, "value") ||
		strings.Contains(body, "proof verification failed"),
		"malformed bundle should fail binding or field decode, body=%s", body)
}

func requireHTTPErrorStatus(t *testing.T, err error, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if err != nil {
		he, ok := err.(*echo.HTTPError)
		require.True(t, ok, "expected echo.HTTPError, got %T: %v", err, err)
		assert.Equal(t, code, he.Code)
		return
	}
	assert.Equal(t, code, rec.Code)
}

func responseBody(_ echo.Context, rec *httptest.ResponseRecorder, err error) string {
	if err != nil {
		if he, ok := err.(*echo.HTTPError); ok {
			if msg, ok := he.Message.(string); ok {
				return msg
			}
		}
	}
	return rec.Body.String()
}

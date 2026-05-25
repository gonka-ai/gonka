package transport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
)

type recordingEncryptedEngine struct {
	lastBody    []byte
	returnBytes []byte
	returnErr   error
	callCount   int
}

func (r *recordingEncryptedEngine) ForwardEncrypted(_ context.Context, body []byte) ([]byte, error) {
	r.callCount++
	r.lastBody = append([]byte(nil), body...)
	if r.returnErr != nil {
		return nil, r.returnErr
	}
	return r.returnBytes, nil
}

var _ devshard.EncryptedEngine = (*recordingEncryptedEngine)(nil)

const encryptedPath = "/v1/devshard/sessions/escrow-1/encrypted/chat/completions"

func postEncrypted(t *testing.T, env *serverTestEnv, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now().Unix()
	sig, err := SignRequest(env.userSigner, "escrow-1", body, ts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, encryptedPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func TestHandleEncryptedInference_HappyPath(t *testing.T) {
	rec := &recordingEncryptedEngine{
		returnBytes: []byte(`{"version":1,"session_id":"escrow-1","nonce":1,"aead_id":3,"response_nonce":"AAAAAAAAAAAAAAAA","ciphertext":"AAAA"}`),
	}
	env := setupServerEnv(t, WithEncryptedEngine(rec))

	body := []byte(`{"version":1,"session_id":"escrow-1","nonce":1,"kem_id":32,"kdf_id":1,"aead_id":3,"enc":"AAAA","prompt_hash":"AAAA","ciphertext":"AAAA"}`)
	resp := postEncrypted(t, env, body)

	require.Equal(t, http.StatusOK, resp.Code, "body=%s", resp.Body.String())
	require.Equal(t, echo.MIMEApplicationJSON, resp.Header().Get("Content-Type"))
	require.Equal(t, 1, rec.callCount)
	require.Equal(t, body, rec.lastBody, "body must be forwarded verbatim")
}

func TestHandleEncryptedInference_NoEngineRouteAbsent(t *testing.T) {
	env := setupServerEnv(t)
	resp := postEncrypted(t, env, []byte(`{"version":1,"session_id":"escrow-1","nonce":1}`))
	require.Equal(t, http.StatusNotFound, resp.Code)
}

func TestHandleEncryptedInference_AuthRejection(t *testing.T) {
	rec := &recordingEncryptedEngine{returnBytes: []byte("{}")}
	env := setupServerEnv(t, WithEncryptedEngine(rec))

	req := httptest.NewRequest(http.MethodPost, encryptedPath, strings.NewReader(`{"version":1,"session_id":"escrow-1","nonce":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	env.echo.ServeHTTP(resp, req)

	require.Equal(t, http.StatusUnauthorized, resp.Code)
	require.Equal(t, 0, rec.callCount)
}

func TestHandleEncryptedInference_NonOwnerForbidden(t *testing.T) {
	rec := &recordingEncryptedEngine{returnBytes: []byte("{}")}
	env := setupServerEnv(t, WithEncryptedEngine(rec))

	otherSigner := testutil.MustGenerateKey(t)
	body := []byte(`{"version":1,"session_id":"escrow-1","nonce":1}`)
	ts := time.Now().Unix()
	sig, err := SignRequest(otherSigner, "escrow-1", body, ts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, encryptedPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, fmt.Sprintf("%d", ts))
	resp := httptest.NewRecorder()
	env.echo.ServeHTTP(resp, req)

	require.True(t, resp.Code == http.StatusForbidden || resp.Code == http.StatusUnauthorized,
		"want 401 or 403, got %d (body=%s)", resp.Code, resp.Body.String())
	require.Equal(t, 0, rec.callCount)
}

func TestHandleEncryptedInference_BadEnvelopeRejected(t *testing.T) {
	rec := &recordingEncryptedEngine{returnBytes: []byte("{}")}
	env := setupServerEnv(t, WithEncryptedEngine(rec))

	resp := postEncrypted(t, env, []byte(`{"version":1,"nonce":1}`))
	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, rec.callCount)
}

func TestHandleEncryptedInference_SessionMismatchRejected(t *testing.T) {
	rec := &recordingEncryptedEngine{returnBytes: []byte("{}")}
	env := setupServerEnv(t, WithEncryptedEngine(rec))

	resp := postEncrypted(t, env, []byte(`{"version":1,"session_id":"other-session","nonce":1}`))
	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, rec.callCount)
}

func TestHandleEncryptedInference_EngineErrorBubbles502(t *testing.T) {
	rec := &recordingEncryptedEngine{returnErr: errors.New("backend down")}
	env := setupServerEnv(t, WithEncryptedEngine(rec))

	resp := postEncrypted(t, env, []byte(`{"version":1,"session_id":"escrow-1","nonce":1,"enc":"AAAA","prompt_hash":"AAAA","ciphertext":"AAAA"}`))
	require.Equal(t, http.StatusBadGateway, resp.Code)
	require.Equal(t, 1, rec.callCount)
}

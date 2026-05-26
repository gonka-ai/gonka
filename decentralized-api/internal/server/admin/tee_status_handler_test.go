package admin

import (
	"decentralized-api/cosmosclient"
	"decentralized-api/poc"
	"decentralized-api/teeverify"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTeeStatus_WorkerNilReportsDisabled(t *testing.T) {
	s, _, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/tee-status", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	require.NoError(t, s.getTeeStatus(c))

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp TeeStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.Enabled)
	assert.NotEmpty(t, resp.Reason)
	assert.Nil(t, resp.Worker)
	assert.Empty(t, resp.Verifiers)
}

func TestGetTeeStatus_DisabledWorkerReportsDisabled(t *testing.T) {
	s, _, _ := setupTestServer(t)
	s.teeWorker = poc.NewTeeAttestationWorker(
		&cosmosclient.MockCosmosMessageClient{},
		nil,
		nil,
		nil,
		0,
	)
	s.teeVerifiers = teeverify.NewRegistry()
	t.Cleanup(func() { s.teeWorker.Close() })

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/tee-status", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	require.NoError(t, s.getTeeStatus(c))

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp TeeStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.Enabled)
	assert.Contains(t, resp.Reason, "tee attestation worker is disabled")
	if assert.NotNil(t, resp.Worker) {
		assert.False(t, resp.Worker.Enabled)
		assert.NotEmpty(t, resp.Worker.DisabledReason)
	}
}

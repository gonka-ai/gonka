//go:build !devshard_testenv

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Point the testenv fault directory at a temp dir and create the public-down trip file in it.
// 2. Build the public server with a ready lifecycle.
// 3. Require /healthz to answer 200, because the production build never reads the trip file.
func TestPublicFaultProdIgnoresTripFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "devshard-fault-public-down"), nil, 0o600))
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
}

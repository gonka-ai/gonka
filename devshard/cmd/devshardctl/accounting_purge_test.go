package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/accounting"
	"devshard/types"
)

func purgeGateway(t *testing.T) (*Gateway, *accounting.Tracker) {
	t.Helper()
	tracker, err := accounting.OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tracker.Close()) })
	require.NoError(t, tracker.RegisterEscrow(accounting.EscrowMetadata{
		EscrowID: "e1", CreationEpoch: 9, Model: "m",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "p0"}, {SlotID: 1, ValidatorAddress: "p1"}},
	}))
	return &Gateway{accounting: accounting.NewRecorder(tracker, nil)}, tracker
}

// The accounting listener has no authentication of its own, so a destructive route belongs behind the
// admin key or nowhere.
func TestAdminAccountingPurge_IsReachableOnlyWithTheAdminKey(t *testing.T) {
	handler := adminAuthMiddleware("admin-key", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodPost, "/v1/admin/accounting/purge", strings.NewReader(`{"epoch":9}`)))
	require.Equal(t, http.StatusUnauthorized, anonymous.Code)

	authorized := httptest.NewRequest(http.MethodPost, "/v1/admin/accounting/purge", strings.NewReader(`{"epoch":9}`))
	authorized.Header.Set("Authorization", "Bearer admin-key")
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, authorized)
	require.Equal(t, http.StatusTeapot, allowed.Code, "the admin key must reach the handler")
}

func TestAdminAccountingPurge_DiscardsTheNamedEpoch(t *testing.T) {
	gateway, tracker := purgeGateway(t)
	require.NotEmpty(t, tracker.Query(accounting.QueryFilter{EpochIndex: 9}), "precondition")

	recorder := httptest.NewRecorder()
	gateway.handleAdminAccountingPurge(recorder, httptest.NewRequest(http.MethodPost, "/v1/admin/accounting/purge", strings.NewReader(`{"epoch":9}`)))

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Epoch          uint64 `json:"epoch"`
		EscrowsRemoved int    `json:"escrows_removed"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, uint64(9), body.Epoch)
	require.Equal(t, 1, body.EscrowsRemoved)
	require.Empty(t, tracker.Query(accounting.QueryFilter{EpochIndex: 9}))
}

// An absent epoch field decodes to zero; answering that with a wipe would be the worst possible default.
func TestAdminAccountingPurge_RejectsAMissingEpoch(t *testing.T) {
	gateway, tracker := purgeGateway(t)

	recorder := httptest.NewRecorder()
	gateway.handleAdminAccountingPurge(recorder, httptest.NewRequest(http.MethodPost, "/v1/admin/accounting/purge", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.NotEmpty(t, tracker.Query(accounting.QueryFilter{EpochIndex: 9}), "nothing was discarded")
}

func TestAdminAccountingPurge_RefusesAnythingButPost(t *testing.T) {
	gateway, _ := purgeGateway(t)

	recorder := httptest.NewRecorder()
	gateway.handleAdminAccountingPurge(recorder, httptest.NewRequest(http.MethodGet, "/v1/admin/accounting/purge", nil))

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
}

// A gateway with DEVSHARD_STATS_ENABLED=false has no ledger to discard.
func TestAdminAccountingPurge_SaysSoWhenAccountingIsOff(t *testing.T) {
	gateway := &Gateway{accounting: accounting.NewRecorder(nil, nil)}

	recorder := httptest.NewRecorder()
	gateway.handleAdminAccountingPurge(recorder, httptest.NewRequest(http.MethodPost, "/v1/admin/accounting/purge", strings.NewReader(`{"epoch":9}`)))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

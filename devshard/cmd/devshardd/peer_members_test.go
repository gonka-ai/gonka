package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminPeerRPCMembers(t *testing.T) {
	lifecycle := newLifecycleState()
	admin := buildAdminServer(lifecycle, func() bool { return true }, nil, recoveryDone, nil)
	var gotID string
	var got []string
	registerPeerRPCMembers(admin, func(instanceID string, ids []string) {
		gotID = instanceID
		got = append([]string(nil), ids...)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/internal/peer-rpc/members", strings.NewReader(
		`{"instance_id":"versiond1@v2","members":[{"id":"versiond1@v2"},{"id":"versiond2@v2"}]}`))
	admin.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "versiond1@v2", gotID)
	require.Equal(t, []string{"versiond1@v2", "versiond2@v2"}, got)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/internal/peer-rpc/members", strings.NewReader(`{`))
	admin.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

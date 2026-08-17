package adminface_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/seed"

	"github.com/stretchr/testify/require"
)

func TestAdminface_ParamsAndEpoch(t *testing.T) {
	st := seed.Defaults()
	srv := adminface.NewServer(st, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	maxNonce := uint32(555)
	body, _ := json.Marshal(adminface.ParamsRequest{MaxNonce: &maxNonce})
	resp, err := http.Post(ts.URL+"/testenv/params", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()
	require.Equal(t, uint32(555), st.GetParams().DevshardEscrowParams.MaxNonce)

	client := adminface.NewClient(ts.URL)
	require.NoError(t, client.PatchEpoch(context.Background(), adminface.EpochRequest{Advance: true}))
	require.Equal(t, uint64(2), st.GetEpoch().Index)
}

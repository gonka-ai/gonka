package grpcface_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/seed"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Seed the default store and serve mock-chain gRPC plus the admin routes.
//  2. Require DevshardEscrow to find a seeded escrow.
//  3. POST /testenv/escrow-query-fault absent; require the same query to answer found=false without an error.
//  4. Clear the fault; require the escrow to be found again.
func TestMockChainGRPC_EscrowQueryAbsent(t *testing.T) {
	st := seed.Defaults()
	escrows := st.ListEscrows()
	require.NotEmpty(t, escrows)
	escrowID := escrows[0].GetId()
	client := startTestServer(t, st)
	admin := httptest.NewServer(adminface.NewServer(st, nil, nil).Handler())
	t.Cleanup(admin.Close)
	ctx := context.Background()

	setFault := func(body string) {
		resp, err := http.Post(admin.URL+"/testenv/escrow-query-fault", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	found := func() bool {
		resp, err := client.InferenceQueryClient().DevshardEscrow(ctx, &inferencetypes.QueryGetDevshardEscrowRequest{Id: escrowID})
		require.NoError(t, err)
		return resp.GetFound()
	}

	require.True(t, found())
	setFault(`{"absent":true}`)
	require.False(t, found())
	setFault(`{}`)
	require.True(t, found())
}

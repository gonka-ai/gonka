package grpcface_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/chain"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/store"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Test flow:
// 1. Seed two participants and serve mock-chain gRPC plus the admin routes.
// 2. POST /testenv/participant unavailable for the target; require a transient (DeadlineExceeded) error for it and success for the other.
// 3. Switch the target to absent; require NotFound.
// 4. Switch it back to ok; require the participant to be returned again.
// 5. Require an unknown mode and a missing address to be rejected with 400.
func TestMockChainGRPC_ParticipantModes(t *testing.T) {
	st := seed.Defaults()
	target := "gonka1target0000000000000000000000000000000"
	other := "gonka1host000000000000000000000000000000000"
	st.Participants[target] = &inferencetypes.Participant{Index: target, Address: target}
	client := startTestServer(t, st)
	admin := httptest.NewServer(adminface.NewServer(st, nil, nil).Handler())
	t.Cleanup(admin.Close)
	ctx := context.Background()

	setMode := func(body string) int {
		resp, err := http.Post(admin.URL+"/testenv/participant", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	query := func(address string) (*inferencetypes.QueryGetParticipantResponse, error) {
		return client.InferenceQueryClient().Participant(ctx, &inferencetypes.QueryGetParticipantRequest{Index: address})
	}

	require.Equal(t, http.StatusOK, setMode(`{"address":"`+target+`","mode":"unavailable"}`))
	_, err := query(target)
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	resp, err := query(other)
	require.NoError(t, err)
	require.Equal(t, other, resp.Participant.Address)

	require.Equal(t, http.StatusOK, setMode(`{"address":"`+target+`","mode":"absent"}`))
	_, err = query(target)
	require.Equal(t, codes.NotFound, status.Code(err))

	require.Equal(t, http.StatusOK, setMode(`{"address":"`+target+`","mode":"ok"}`))
	resp, err = query(target)
	require.NoError(t, err)
	require.Equal(t, target, resp.Participant.Address)

	require.Equal(t, http.StatusBadRequest, setMode(`{"address":"`+target+`","mode":"gone"}`))
	require.Equal(t, http.StatusBadRequest, setMode(`{"mode":"absent"}`))
}

// Test flow:
// 1. Serve mock-chain gRPC and CometBFT RPC; dial them with the production gRPC-to-RPC fallback client.
// 2. Mark one participant unavailable and query it; require the query to fail.
// 3. Query epoch info and params afterwards; require both to succeed, so the client stayed on gRPC.
func TestMockChainGRPC_ParticipantUnavailableKeepsQueriesOnGRPC(t *testing.T) {
	st := seed.Defaults()
	target := "gonka1target0000000000000000000000000000000"
	st.Participants[target] = &inferencetypes.Participant{Index: target, Address: target}
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	_, rpcURL, stopRPC, err := rpcface.NewInProcessServer(st, rpcface.Config{})
	require.NoError(t, err)
	t.Cleanup(stopRPC)
	client, err := chain.NewWithRPCFallback(lis.Addr().String(), rpcURL)
	require.NoError(t, err)
	ctx := context.Background()

	st.SetParticipantMode(target, store.ParticipantModeUnavailable)
	_, err = client.InferenceQueryClient().Participant(ctx, &inferencetypes.QueryGetParticipantRequest{Index: target})
	require.Error(t, err)

	_, err = client.InferenceQueryClient().EpochInfo(ctx, &inferencetypes.QueryEpochInfoRequest{})
	require.NoError(t, err, "a participant-level fault must not move the client off gRPC")
	_, err = client.InferenceQueryClient().Params(ctx, &inferencetypes.QueryParamsRequest{})
	require.NoError(t, err)
}

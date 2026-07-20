package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/types"
)

func TestMockChain_RESTContract(t *testing.T) {
	cfg := mockConfig{
		EscrowID:       "42",
		CreatorAddress: "inference1owner",
		Amount:         5000,
		EpochID:        7,
		AppHash:        "deadbeef",
		TokenPrice:     3,
		Hosts: []hostConfig{
			{Address: "valA", URL: "http://host-a:8080"},
			{Address: "valB", URL: "http://host-b:8080"},
		},
		ValidationThreshold: bridge.Decimal{Value: 958, Exponent: -3},
		WarmGrants: map[string][]string{
			"valA": {"warmA"},
		},
	}
	srv := httptest.NewServer(newHandler(cfg))
	t.Cleanup(srv.Close)

	var escrow escrowResponse
	getJSON(t, srv.URL+"/productscience/inference/inference/devshard_escrow/42", &escrow)
	require.True(t, escrow.Found)
	require.Equal(t, "42", escrow.Escrow.ID)
	require.Equal(t, "5000", escrow.Escrow.Amount)
	require.Equal(t, "inference1owner", escrow.Escrow.Creator)
	require.Equal(t, []string{"valA", "valB"}, escrow.Escrow.Slots)
	require.Equal(t, "7", escrow.Escrow.EpochIndex)
	require.Equal(t, "3", escrow.Escrow.TokenPrice)

	var participant participantResponse
	getJSON(t, srv.URL+"/productscience/inference/inference/participant/valB", &participant)
	require.Equal(t, "valB", participant.Participant.Address)
	require.Equal(t, "http://host-b:8080", participant.Participant.InferenceURL)

	var epoch epochGroupDataResponse
	getJSON(t, srv.URL+"/productscience/inference/inference/epoch_group_data/7", &epoch)
	require.NotNil(t, epoch.EpochGroupData.ModelSnapshot)
	require.Equal(t, int64(958), epoch.EpochGroupData.ModelSnapshot.ValidationThreshold.Value)
	require.Equal(t, int32(-3), epoch.EpochGroupData.ModelSnapshot.ValidationThreshold.Exponent)

	var params paramsResponse
	getJSON(t, srv.URL+"/productscience/inference/inference/params", &params)
	defaults := types.DefaultSessionConfig(len(cfg.Hosts))
	require.Equal(t, "60", params.Params.DevshardEscrowParams.RefusalTimeout)
	require.Equal(t, strconv.FormatInt(defaults.ExecutionTimeout, 10), params.Params.DevshardEscrowParams.ExecutionTimeout)
	require.Equal(t, defaults.ValidationRate, params.Params.DevshardEscrowParams.ValidationRate)
	require.Equal(t, uint32(50), params.Params.DevshardEscrowParams.VoteThresholdFactor)

	var grantees granteesResponse
	getJSON(t, srv.URL+"/productscience/inference/inference/grantees_by_message_type/valA/MsgStartInference", &grantees)
	require.Equal(t, []granteeJSON{{Address: "warmA"}}, grantees.Grantees)

	getJSON(t, srv.URL+"/productscience/inference/inference/grantees_by_message_type/valB/MsgStartInference", &grantees)
	require.Empty(t, grantees.Grantees)
}

func TestMockChain_NotFoundResponses(t *testing.T) {
	srv := httptest.NewServer(newHandler(defaultConfig()))
	t.Cleanup(srv.Close)

	var escrow escrowResponse
	getJSON(t, srv.URL+"/productscience/inference/inference/devshard_escrow/missing", &escrow)
	require.False(t, escrow.Found)

	resp, err := http.Get(srv.URL + "/productscience/inference/inference/participant/missing")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestMockChain_Health(t *testing.T) {
	srv := httptest.NewServer(newHandler(defaultConfig()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func getJSON(t *testing.T, url string, dest any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, dest), "body=%s", string(body))
}

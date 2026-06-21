package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
)

func TestMockChain_RESTBridgeContract(t *testing.T) {
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
			"valA": []string{"warmA"},
		},
	}
	srv := httptest.NewServer(newHandler(cfg))
	defer srv.Close()

	client := bridge.NewRESTBridge(srv.URL)

	escrow, err := client.GetEscrow("42")
	require.NoError(t, err)
	require.Equal(t, "42", escrow.EscrowID)
	require.Equal(t, uint64(5000), escrow.Amount)
	require.Equal(t, "inference1owner", escrow.CreatorAddress)
	require.Equal(t, []string{"valA", "valB"}, escrow.Slots)
	require.Equal(t, uint64(7), escrow.EpochID)
	require.Equal(t, uint64(3), escrow.TokenPrice)

	host, err := client.GetHostInfo("valB")
	require.NoError(t, err)
	require.Equal(t, "valB", host.Address)
	require.Equal(t, "http://host-b:8080", host.URL)

	threshold, err := client.GetValidationThreshold(7, "stub-model")
	require.NoError(t, err)
	require.Equal(t, int64(958), threshold.Value)
	require.Equal(t, int32(-3), threshold.Exponent)

	ok, err := client.VerifyWarmKey("warmA", "valA")
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = client.VerifyWarmKey("warmB", "valA")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestMockChain_NotFoundResponses(t *testing.T) {
	srv := httptest.NewServer(newHandler(defaultConfig()))
	defer srv.Close()

	client := bridge.NewRESTBridge(srv.URL)

	_, err := client.GetEscrow("missing")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)

	_, err = client.GetHostInfo("missing")
	require.ErrorIs(t, err, bridge.ErrParticipantNotFound)
}

func TestMockChain_Health(t *testing.T) {
	srv := httptest.NewServer(newHandler(defaultConfig()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

package loadtest

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAddressInUse(t *testing.T) {
	require.True(t, isAddressInUse(errors.New("failed to set up container networking: Address already in use")))
	require.False(t, isAddressInUse(errors.New("container is unhealthy")))
	require.False(t, isAddressInUse(nil))
}

func TestStripStaticNetworking(t *testing.T) {
	compose := `networks:
  testenv:
    driver: bridge
    ipam:
      config:
        - subnet: 172.31.42.0/24
services:
  mock-chain:
    networks:
      testenv:
        ipv4_address: 172.31.42.2
`

	actual := stripStaticNetworking(compose)
	require.NotContains(t, actual, "ipam:")
	require.NotContains(t, actual, "subnet:")
	require.NotContains(t, actual, "ipv4_address:")
	require.Contains(t, actual, "driver: bridge")
}

func TestFormatStatusCounts(t *testing.T) {
	require.Equal(t, "finished=3, started=1", formatStatusCounts(map[string]int{
		"started":  1,
		"finished": 3,
	}))
	require.Equal(t, "unavailable", formatStatusCounts(nil))
}

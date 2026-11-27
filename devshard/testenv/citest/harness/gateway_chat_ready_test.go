package harness

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayCapacityGone(t *testing.T) {
	require.False(t, GatewayCapacityGone(nil))
	require.False(t, GatewayCapacityGone(fmt.Errorf("timeout")))
	require.True(t, GatewayCapacityGone(fmt.Errorf("no devshard runtimes available for new inferences")))
	require.True(t, GatewayCapacityGone(fmt.Errorf("prepare: local apply: mandatory start inference: insufficient escrow balance")))
}

func TestRouterCatalogHealthzURL(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:9/v2/healthz",
		routerCatalogHealthzURL("http://127.0.0.1:9", "v2"))
	require.Equal(t, "http://127.0.0.1:9/v2/healthz",
		routerCatalogHealthzURL("http://127.0.0.1:9/", "v2"))
}

// Test flow:
// 1. Judge status listings with no escrow, an escrow with no id and an escrow with an id.
// 2. Check only the listing with an identified escrow counts as routable.
func TestGatewayStatusListsRoutableEscrow(t *testing.T) {
	require.False(t, gatewayStatusListsRoutableEscrow(nil))
	require.False(t, gatewayStatusListsRoutableEscrow([]gatewayStatusEscrow{}))
	require.False(t, gatewayStatusListsRoutableEscrow([]gatewayStatusEscrow{{EscrowID: noEscrowID}}))
	require.True(t, gatewayStatusListsRoutableEscrow([]gatewayStatusEscrow{{EscrowID: "1"}}))
}

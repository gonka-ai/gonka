package harness

import "testing"

func TestRequireGatewaySessionStable_AllowsHeartbeatAdvance(t *testing.T) {
	before := GatewaySessionSnapshot{
		EscrowID: "1", SessionNonce: 4, LatestNonce: 4, Balance: 100, Phase: "active",
	}
	after := GatewaySessionSnapshot{
		EscrowID: "1", SessionNonce: 7, LatestNonce: 7, Balance: 97, Phase: "active",
	}
	RequireGatewaySessionStable(t, before, after)
}

func TestRequireGatewaySessionStable_AllowsTimeoutRefund(t *testing.T) {
	before := GatewaySessionSnapshot{
		EscrowID: "1", SessionNonce: 4, LatestNonce: 4, Balance: 955400, Phase: "active",
	}
	after := GatewaySessionSnapshot{
		EscrowID: "1", SessionNonce: 7, LatestNonce: 7, Balance: 972200, Phase: "active",
	}
	RequireGatewaySessionStable(t, before, after)
}

func TestRequireGatewaySessionStable_EqualIsOK(t *testing.T) {
	snap := GatewaySessionSnapshot{
		EscrowID: "1", SessionNonce: 4, LatestNonce: 4, Balance: 100, Phase: "active",
	}
	RequireGatewaySessionStable(t, snap, snap)
}

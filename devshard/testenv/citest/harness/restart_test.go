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

func TestGatewaySessionLedgerQuiet_IgnoresLiveInferences(t *testing.T) {
	prev := GatewaySessionSnapshot{
		EscrowID: "1", SessionNonce: 1, LatestNonce: 1, Balance: 961700, LiveInferences: 1,
	}
	cur := prev
	cur.LiveInferences = 1
	if !gatewaySessionLedgerQuiet(prev, cur) {
		t.Fatal("Finished inferences remaining live must not block settle")
	}
	cur.Balance = 972200
	if gatewaySessionLedgerQuiet(prev, cur) {
		t.Fatal("a timeout-refund changing balance must not look settled")
	}
	cur = prev
	cur.SessionNonce = 2
	if gatewaySessionLedgerQuiet(prev, cur) {
		t.Fatal("a heartbeat advancing nonce must not look settled")
	}
}

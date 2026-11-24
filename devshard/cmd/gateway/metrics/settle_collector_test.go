package metrics

import "testing"

// Test flow:
//  1. Register a `SettleCollector` backed by a source function that returns 4.
//  2. Assert the settled-unresolved-nonces counter reports 4.
func TestTheSettleCollectorReportsTheNoncesAForcedSettleFinalizedPast(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewSettleCollector(func() uint64 { return 4 }))

	expectCounter(t, telemetry, "devshard_gateway_escrow_settled_unresolved_nonces_total", labels{}, 4)
}

// Test flow:
//  1. Register a `SettleCollector` with a nil source function.
//  2. Assert the settled-unresolved-nonces series count is zero.
func TestASettleCollectorWithNoSourceReportsNothing(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewSettleCollector(nil))

	expectSeriesCount(t, telemetry, "devshard_gateway_escrow_settled_unresolved_nonces_total", 0)
}

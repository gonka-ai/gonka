package metrics

import "testing"

// Test flow:
//  1. Register an `UnopenedCollector` whose source lists two unopened escrows, one with a reason outside the enum and one with an empty model.
//  2. Assert each escrow has one series under its id and model, the unknown reason is folded to `other`, and the empty model reads `unknown`.
//  3. Empty the source and assert the family has no series.
func TestTheUnopenedCollectorReportsEachListedEscrowAndNothingAfterItLeaves(t *testing.T) {
	listed := []UnopenedEscrow{
		{EscrowID: "7", Model: "qwen", Reason: UnopenedLocalState},
		{EscrowID: "9", Model: "", Reason: "open /var/lib/gonka/9: permission denied"},
	}
	telemetry := New()
	telemetry.Register(NewUnopenedCollector(func() []UnopenedEscrow { return listed }))

	expectGauge(t, telemetry, "devshard_gateway_escrow_unopened", labels{"devshard_id": "7", "model": "qwen", "reason": "local_state_unrecoverable"}, 1)
	expectGauge(t, telemetry, "devshard_gateway_escrow_unopened", labels{"devshard_id": "9", "model": "unknown", "reason": "other"}, 1)
	expectSeriesCount(t, telemetry, "devshard_gateway_escrow_unopened", 2)

	listed = nil
	expectAbsent(t, telemetry, "devshard_gateway_escrow_unopened")
}

// Test flow:
//  1. Register an `UnopenedCollector` with a nil source.
//  2. Assert the family has no series.
func TestAnUnopenedCollectorWithoutASourceEmitsNothing(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewUnopenedCollector(nil))

	expectAbsent(t, telemetry, "devshard_gateway_escrow_unopened")
}

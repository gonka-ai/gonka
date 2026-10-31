package metrics

import "testing"

// Test flow:
//  1. Build a `DispatchRecorder`.
//  2. Record two ghost burns for one reason and one for another, plus one nonce hold and one exhausted burn budget, across two escrow ids.
//  3. Assert each ghost-burn reason, the nonce-hold count, and the exhausted-budget count are each counted separately by their labels.
func TestTheDispatchRecorderCountsEachNonceOutcomeSeparately(t *testing.T) {
	telemetry := New()
	recorder := NewDispatchRecorder(telemetry)

	recorder.GhostBurned("7", "gonka1host", "poc_unavailable_host")
	recorder.GhostBurned("7", "gonka1host", "poc_unavailable_host")
	recorder.GhostBurned("7", "gonka1host", "participant_window_full_no_send")
	recorder.NonceHeld("7")
	recorder.BurnBudgetExhausted("9")

	expectCounter(t, telemetry, "devshard_gateway_ghost_nonces_burned_total", labels{"devshard_id": "7", "participant": "gonka1host", "reason": "poc_unavailable_host"}, 2)
	expectCounter(t, telemetry, "devshard_gateway_ghost_nonces_burned_total", labels{"devshard_id": "7", "participant": "gonka1host", "reason": "participant_window_full_no_send"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_nonce_holds_total", labels{"devshard_id": "7"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_burn_budget_exhausted_total", labels{"devshard_id": "9"}, 1)
}

// Test flow:
//  1. Build a `DispatchRecorder` and record a ghost burn, a nonce hold, and an exhausted burn budget for one escrow, plus a ghost burn for a second escrow.
//  2. Retire the first escrow via `EscrowRetired`.
//  3. Gather every metric family and assert none still carries a `devshard_id` label for the retired escrow.
func TestDispatchRecorderDropsSeriesWhenAnEscrowRetires(t *testing.T) {
	telemetry := New()
	recorder := NewDispatchRecorder(telemetry)

	recorder.GhostBurned("escrow-1", "gonka1host", "poc_unavailable_host")
	recorder.NonceHeld("escrow-1")
	recorder.BurnBudgetExhausted("escrow-1")
	recorder.GhostBurned("escrow-2", "gonka1host", "participant_window_full_no_send")

	recorder.EscrowRetired("escrow-1")

	families, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather(): %v", err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "devshard_id" && label.GetValue() == "escrow-1" {
					t.Fatalf("%s still carries a series for the retired escrow", family.GetName())
				}
			}
		}
	}
}

// Test flow:
//  1. Build a `DispatchRecorder` and record a ghost burn, a nonce hold, and an exhausted burn budget using an escrow id padded with spaces.
//  2. Retire the escrow via `EscrowRetired`, passing the same padded id.
//  3. Assert every series for that escrow was dropped, proving retirement trims the id the same way the writes did.
func TestDispatchRecorderDropsTheSeriesItWroteForAnUntrimmedEscrowID(t *testing.T) {
	telemetry := New()
	recorder := NewDispatchRecorder(telemetry)
	recorder.GhostBurned(" escrow-1 ", "gonka1host", "poc_unavailable_host")
	recorder.NonceHeld(" escrow-1 ")
	recorder.BurnBudgetExhausted(" escrow-1 ")

	recorder.EscrowRetired(" escrow-1 ")

	expectSeriesCount(t, telemetry, "devshard_gateway_ghost_nonces_burned_total", 0)
	expectSeriesCount(t, telemetry, "devshard_gateway_nonce_holds_total", 0)
	expectSeriesCount(t, telemetry, "devshard_gateway_burn_budget_exhausted_total", 0)
}

package accounting

import (
	"testing"

	"devshard/cmd/gateway/engine"
)

// Test flow:
//  1. Build a counter key with terminal `engine.ReasonRequestTooLarge`.
//  2. Assert `excused` reports it excused from the host's failure rate.
//  3. Assert `offRecord` reports it kept off the record.
func TestABodyTheGatewayRefusedToSendLeavesTheHostsRatesAlone(t *testing.T) {
	t.Parallel()

	refused := CounterKey{Terminal: engine.ReasonRequestTooLarge}

	if !excused(refused) {
		t.Fatal("a body the gateway refused to send counted against the host's failure rate")
	}
	if !offRecord(refused) {
		t.Fatal("a body the gateway refused to send was kept on the record")
	}
}

// Test flow:
//  1. Build a counter key with terminal `engine.ReasonEscrowLookupLimited`.
//  2. Assert `excused` reports it excused from the host's failure rate.
func TestAnEscrowLookupRefusalLeavesTheHostsFailureRateAlone(t *testing.T) {
	t.Parallel()

	refused := CounterKey{Terminal: engine.ReasonEscrowLookupLimited}

	if !excused(refused) {
		t.Fatal("excused(escrow lookup limited) = false, want true: strangers spent the host's lookup budget")
	}
}

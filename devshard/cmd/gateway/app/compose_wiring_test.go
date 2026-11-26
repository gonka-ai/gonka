package app

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

// Test flow:
//  1. Build the host capability lookup the nonce ledger reads, over a real host tracker.
//  2. Record two output overruns for one participant and model on the tracker.
//  3. Assert the capability the ledger reads for that participant and model carries both overruns.
func TestTheLedgersHostCapabilityCarriesTheTrackersOutputOverruns(t *testing.T) {
	configuration := config.Defaults()
	hosts := perf.NewTracker(config.NewHolder(&configuration), fixedClock)
	participants := limits.NewParticipantLimiter(limits.ParticipantConfigFromLimits(configuration.Limits), fixedClock)
	lookup := hostCapabilities(hosts, participants, limits.NewCapacity(participants.Available))

	hosts.RecordOutputOverrun("validator-a", "model-a")
	hosts.RecordOutputOverrun("validator-a", "model-a")

	if got := lookup("validator-a", "model-a").OutputOverruns; got != 2 {
		t.Fatalf("capability output overruns = %d, want 2", got)
	}
}

// Test flow:
//  1. Compose a gateway and take the context its HTTP server hands each request.
//  2. Call the request-ending func the shutdown steps receive.
//  3. Assert the request context is cancelled, so a stream still open after the graceful share ends.
func TestTheShutdownStepsEndTheRequestsTheServerStarted(t *testing.T) {
	gatewayEnvironment(t)
	composed := composedGateway(t)
	requestCtx := composed.server.BaseContext(nil)
	endRequests := composed.partsToStop().endRequests
	if endRequests == nil {
		t.Fatal("the shutdown steps carry no request-ending func")
	}

	endRequests()

	if !errors.Is(requestCtx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v, want context.Canceled", requestCtx.Err())
	}
}

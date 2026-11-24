package app

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/store"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
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

// Test flow:
//  1. Set the gateway's own Version to a value that is not the devshard protocol version.
//  2. Compose a gateway over a session source that records the host route prefix it is handed.
//  3. Assert the prefix is /devshard/ plus the protocol version, not the gateway Version.
func TestTheHostRoutePrefixComesFromTheProtocolVersionNotTheGatewayVersion(t *testing.T) {
	gatewayEnvironment(t)
	previous := Version
	Version = "gateway-" + types.EffectiveStateRootAndProtocolVersion + "-own"
	t.Cleanup(func() { Version = previous })
	values, err := env.Load()
	if err != nil {
		t.Fatalf("loading environment: %v", err)
	}
	storageDir, err := resolveStorageDir(values.StorageDir)
	if err != nil {
		t.Fatalf("resolving storage dir: %v", err)
	}
	gatewayStore, err := store.Open(storageDir)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	var gotPrefix string
	inner := sessionsReading(gatewayStore, storageDir, chainWithoutADial{})
	recording := func(endpoints config.Chain, heights config.HeightSync, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) (chainSources, error) {
		gotPrefix = routePrefix
		return inner(endpoints, heights, routePrefix, adoption, steps)
	}
	composed, err := compose(context.Background(), values, storageDir, gatewayStore, recording)
	if err != nil {
		gatewayStore.Close()
		t.Fatalf("compose(): %v", err)
	}
	t.Cleanup(func() {
		if err := composed.shutdown(shutdownGracePeriod); err != nil {
			t.Errorf("shutdown(): %v", err)
		}
	})

	want := "/devshard/" + types.EffectiveStateRootAndProtocolVersion
	if gotPrefix != want {
		t.Fatalf("host route prefix = %q with Version %q, want %q", gotPrefix, Version, want)
	}
}

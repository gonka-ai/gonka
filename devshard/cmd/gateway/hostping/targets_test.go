package hostping

import (
	"testing"

	"devshard/cmd/gateway/registry"
)

type dialsHeld []registry.HostDial

func (d dialsHeld) HostDials() []registry.HostDial { return d }

// Test flow:
//  1. Build a Targets source over one live host dial that carries a participant key, base URL and route prefix.
//  2. Call Targets().
//  3. Assert it returns exactly one target keyed by the participant.
//  4. Assert the clock URL and fallback URL are both built under the host's own route prefix.
func TestATargetIsBuiltFromTheHostsOwnPrefix(t *testing.T) {
	source := Targets{Live: dialsHeld{{ParticipantKey: "gonka1abc", BaseURL: "http://a:8080/", RoutePrefix: "/api"}}}

	targets := source.Targets()

	if len(targets) != 1 {
		t.Fatalf("Targets() = %d, want 1", len(targets))
	}
	if targets[0].Key != "gonka1abc" {
		t.Errorf("key = %q, want the participant the metric is labelled by", targets[0].Key)
	}
	if targets[0].ClockURL != "http://a:8080/api/clock" {
		t.Errorf("clock URL = %q", targets[0].ClockURL)
	}
	if targets[0].FallbackURL != "http://a:8080/api/healthz" {
		t.Errorf("fallback URL = %q", targets[0].FallbackURL)
	}
}

// Test flow:
//  1. Build a Targets source over one live host dial that carries no route prefix.
//  2. Call Targets().
//  3. Assert it returns exactly one target.
//  4. Assert the clock URL is not built under an empty prefix, i.e. it falls back to the default one.
func TestAHostWithoutAPrefixTakesTheDefaultOne(t *testing.T) {
	source := Targets{Live: dialsHeld{{ParticipantKey: "gonka1abc", BaseURL: "http://a:8080"}}}

	targets := source.Targets()

	if len(targets) != 1 {
		t.Fatalf("Targets() = %d, want 1", len(targets))
	}
	if got := targets[0].ClockURL; got == "http://a:8080/clock" {
		t.Errorf("clock URL = %q, want it under the default route prefix", got)
	}
}

// Test flow:
//  1. Build a Targets source over an empty (nil) set of live host dials.
//  2. Call Targets().
//  3. Assert it returns no targets.
func TestNothingLiveIsNothingToPing(t *testing.T) {
	source := Targets{Live: dialsHeld(nil)}

	if got := source.Targets(); len(got) != 0 {
		t.Fatalf("Targets() = %+v, want none", got)
	}
}

package types

import "testing"

func TestEffectiveStateRootAndProtocolVersion_default(t *testing.T) {
	t.Setenv("DEVSHARD_VERSION", "must-not-read-env")
	if got := EffectiveStateRootAndProtocolVersion; got != DevshardStateRootAndProtocolVersion {
		t.Fatalf("got %q, want default %q (link-time var empty in go test)", got, DevshardStateRootAndProtocolVersion)
	}
	if DevshardStateRootAndProtocolVersion != "v7" {
		t.Fatalf("unstamped source fallback is %q, want v7", DevshardStateRootAndProtocolVersion)
	}
	if DefaultProtocolVersion != ProtocolV7 {
		t.Fatalf("DefaultProtocolVersion is %q, want %q", DefaultProtocolVersion, ProtocolV7)
	}
	if !StartRequiresFloor(DevshardStateRootAndProtocolVersion) {
		t.Fatal("v7 requires a floor on start")
	}
	if StartRequiresFloor("v6") {
		t.Fatal("v6 still replays an empty first start")
	}
	if !StartRequiresFloor(StartRequiresFloorProtocolVersion) {
		t.Fatal("v7 requires a floor on start")
	}
	if StartRequiresFloor("dev") {
		t.Fatal("a non-numeric tag keeps the earlier rule")
	}
	if !StampRequiresTime(DevshardStateRootAndProtocolVersion) {
		t.Fatal("v7 requires observed_time_ms on stamps")
	}
	if StampRequiresTime("v6") {
		t.Fatal("v6 still accepts zero stamp times")
	}
	if StampRequiresTime("dev") {
		t.Fatal("a non-numeric tag keeps the earlier rule")
	}
}

package types

import "testing"

func TestEffectiveStateRootAndProtocolVersion_default(t *testing.T) {
	t.Setenv("DEVSHARD_VERSION", "must-not-read-env")
	if got := EffectiveStateRootAndProtocolVersion; got != DevshardStateRootAndProtocolVersion {
		t.Fatalf("got %q, want default %q (link-time var empty in go test)", got, DevshardStateRootAndProtocolVersion)
	}
	if DevshardStateRootAndProtocolVersion != "v6" {
		t.Fatalf("unstamped source fallback is %q, want v6", DevshardStateRootAndProtocolVersion)
	}
	if DefaultProtocolVersion != ProtocolV6 {
		t.Fatalf("DefaultProtocolVersion is %q, want %q", DefaultProtocolVersion, ProtocolV6)
	}
}

package observability

import "testing"

func TestOTelEnabledUsesDevshardBooleanGrammar(t *testing.T) {
	t.Setenv(envEnabled, "on")
	if !otelEnabled() {
		t.Fatal("on must enable OpenTelemetry")
	}

	t.Setenv(envEnabled, "f")
	if otelEnabled() {
		t.Fatal("f must disable OpenTelemetry")
	}

	t.Setenv(envEnabled, "invalid")
	if otelEnabled() {
		t.Fatal("invalid value must retain the disabled fallback")
	}
}

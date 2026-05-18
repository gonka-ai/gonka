package debugbuild

import (
	"os"
	"testing"
)

func TestHostDebugRoutesEnabled_requiresEnv(t *testing.T) {
	t.Setenv("DEVSHARDD_DEBUG", "1")
	if !Enabled {
		t.Skip("debug build tag not set")
	}
	if !HostDebugRoutesEnabled() {
		t.Fatal("expected enabled with DEVSHARDD_DEBUG=1")
	}
	t.Setenv("DEVSHARDD_DEBUG", "0")
	if HostDebugRoutesEnabled() {
		t.Fatal("expected disabled without DEVSHARDD_DEBUG=1")
	}
}

func TestCtlDebugRoutesEnabled_requiresEnv(t *testing.T) {
	t.Setenv("DEVSHARDCTL_DEBUG", "1")
	if !Enabled {
		t.Skip("debug build tag not set")
	}
	if !CtlDebugRoutesEnabled() {
		t.Fatal("expected enabled with DEVSHARDCTL_DEBUG=1")
	}
	t.Setenv("DEVSHARDCTL_DEBUG", "")
	if CtlDebugRoutesEnabled() {
		t.Fatal("expected disabled without DEVSHARDCTL_DEBUG=1")
	}
}

func TestDebugRoutesDisabledWithoutBuildTag(t *testing.T) {
	if Enabled {
		t.Skip("built with dev/debug/development tag")
	}
	os.Setenv("DEVSHARDD_DEBUG", "1")
	os.Setenv("DEVSHARDCTL_DEBUG", "1")
	if HostDebugRoutesEnabled() || CtlDebugRoutesEnabled() {
		t.Fatal("env alone must not enable debug routes without debug build tag")
	}
}

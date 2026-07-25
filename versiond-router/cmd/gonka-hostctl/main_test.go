package main

import (
	"path/filepath"
	"testing"
)

func TestHostctlStateDirUsesXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/operator-state")

	got, err := hostctlStateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/operator-state", "gonka", "hostctl")
	if got != want {
		t.Fatalf("hostctl state dir = %q, want %q", got, want)
	}
}

func TestHostctlStateDirRejectsRelativeXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative-state")

	if _, err := hostctlStateDir(); err == nil {
		t.Fatal("relative XDG_STATE_HOME was accepted")
	}
}

func TestHostctlStateDirFallsBackToUserStateDirectory(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/operator-home")

	got, err := hostctlStateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(
		"/tmp/operator-home",
		".local",
		"state",
		"gonka",
		"hostctl",
	)
	if got != want {
		t.Fatalf("hostctl state dir = %q, want %q", got, want)
	}
}

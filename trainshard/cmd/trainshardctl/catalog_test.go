package main

import (
	"strings"
	"testing"
)

// The catalog is built before any config is read, so help and an unknown command answer without
// a key or a chain. A constructor that starts work in New would panic right here
func TestTheCommandCatalogNeedsNothingToBuild(t *testing.T) {
	// act
	commands := catalog()

	// assert
	if len(commands) == 0 {
		t.Fatal("no commands: a tool that lists nothing cannot be asked for help")
	}
	for name := range commands {
		if !strings.Contains(usage(), name) {
			t.Fatalf("command %q is missing from the usage text", name)
		}
	}
}

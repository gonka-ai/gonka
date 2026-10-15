package main

import (
	"strings"
	"testing"
)

func TestTheCommandCatalogBuildsWithoutAKeyOrAChain(t *testing.T) {
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

package main

import (
	"os"
	"testing"

	"devshard/state"
)

func TestMain(m *testing.M) {
	state.SetFixtureProtocolVersion("v6")
	os.Exit(m.Run())
}

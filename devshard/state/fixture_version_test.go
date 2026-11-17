package state

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SetFixtureProtocolVersion("v6")
	os.Exit(m.Run())
}

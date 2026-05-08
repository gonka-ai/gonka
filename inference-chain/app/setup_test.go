package app_test

import (
	"os"
	"testing"

	inferencemodule "github.com/productscience/inference/x/inference/module"
)

// TestMain enables a test-only escape hatch in x/inference/module that
// suppresses the "denom <X> already registered" panic from
// sdk.RegisterDenom when multiple tests in this package each spin up a
// fresh *app.App via createTestApp, NewSimApp, or
// NewSimulationAppInstance. `go test` compiles each package to its own
// binary so the flag dies with the process; production semantics (flag
// default=false) are unaffected. See x/inference/module/genesis.go:16-22.
func TestMain(m *testing.M) {
	inferencemodule.IgnoreDuplicateDenomRegistration = true
	os.Exit(m.Run())
}

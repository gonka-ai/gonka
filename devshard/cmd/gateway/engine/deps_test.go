package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Build a `NewEngine` with an empty `Deps{}`.
//  2. Assert the returned error names the missing `Config` dependency.
func TestNewEngineNamesTheDependencyItWasNotGiven(t *testing.T) {
	_, err := NewEngine(Deps{})

	require.ErrorContains(t, err, "Config")
}

// Test flow:
//  1. Build a complete `Deps` via `completeEngineDeps`, then clear the one required field the table case names.
//  2. Build a `NewEngine` with the incomplete deps.
//  3. Assert the returned error names the missing dependency.
func TestNewEngineRefusesEveryRequiredDependencyByName(t *testing.T) {
	for _, missing := range []string{"Picker", "Targets", "Windows", "Perf", "Snapshots"} {
		t.Run(missing, func(t *testing.T) {
			deps := completeEngineDeps(t)
			switch missing {
			case "Picker":
				deps.Picker = nil
			case "Targets":
				deps.Targets = nil
			case "Windows":
				deps.Windows = nil
			case "Perf":
				deps.Perf = nil
			case "Snapshots":
				deps.Snapshots = nil
			}

			_, err := NewEngine(deps)

			require.ErrorContains(t, err, missing)
		})
	}
}

// Test flow:
//  1. Build a `NewEngine` with `completeEngineDeps`, leaving the optional dependencies unset.
//  2. Assert it returns no error and a non-nil engine.
func TestNewEngineAcceptsTheOptionalDependenciesUnset(t *testing.T) {
	engine, err := NewEngine(completeEngineDeps(t))

	require.NoError(t, err)
	require.NotNil(t, engine)
}

func completeEngineDeps(t *testing.T) Deps {
	t.Helper()
	sim := newSimulator(t, EscalationPolicy{}, 3, "qwen")
	return Deps{
		Picker:    sim.picker,
		Targets:   sim.target,
		Windows:   sim.windows,
		Perf:      sim.perf,
		Snapshots: sim.snapshots,
		Config:    config.NewHolder(engineSettings(EscalationPolicy{}, config.Modes{})),
	}
}

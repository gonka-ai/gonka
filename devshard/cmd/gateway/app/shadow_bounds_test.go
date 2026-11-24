package app

import (
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/shadow"
)

// Test flow:
//  1. Build a config whose global output cap is 4096, with one model capped at 512 and pinned to a 2048-token context, and a chain snapshot where governance gives another model a 8192-token context.
//  2. Ask the probe bounds for each model and for one neither names.
//  3. Assert the pinned model gets its own cap and the pin, the governed model the global cap and governance's length, and the unnamed model the global cap and an unknown (0) context length.
func TestProbeBoundsReadTheModelsCapAndContextLength(t *testing.T) {
	pinned := int64(2048)
	configuration := config.Defaults()
	configuration.Limits.MaxTokensCap = 4096
	pinnedCap := int64(512)
	if err := configuration.ResolveModels(map[string]config.ModelSettings{"pinned": {MaxTokensCap: &pinnedCap, MaxModelLen: &pinned}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	snapshot := chain.PhaseSnapshot{Models: map[string]chain.ModelParams{"governed": {MaxModelLen: 8192}}}
	bounds := probeBounds{configuration: config.NewHolder(&configuration), snapshots: fixedPhase{snapshot}}

	want := map[string]shadow.ModelBounds{
		"pinned":   {OutputCap: 512, ContextLength: 2048},
		"governed": {OutputCap: 4096, ContextLength: 8192},
		"unnamed":  {OutputCap: 4096},
	}
	for model, expected := range want {
		if got := bounds.Of(model); got != expected {
			t.Errorf("Of(%q) = %+v, want %+v", model, got, expected)
		}
	}
}

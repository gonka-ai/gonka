package filters

import (
	"strings"
	"testing"
)

// Test flow:
//  1. Look up the built-in profile for kimiModelID.
//  2. Assert it equals kimiProfile.
func TestBuiltInProfileForMatchesKimi(t *testing.T) {
	profile := BuiltInProfileFor(kimiModelID)
	if profile != kimiProfile {
		t.Fatalf("BuiltInProfileFor(%q) = %v, want kimiProfile", kimiModelID, profile)
	}
}

// Test flow:
//  1. Look up the built-in profile for minimaxModelID.
//  2. Assert it equals minimaxProfile.
func TestBuiltInProfileForMatchesMinimax(t *testing.T) {
	profile := BuiltInProfileFor(minimaxModelID)
	if profile != minimaxProfile {
		t.Fatalf("BuiltInProfileFor(%q) = %v, want minimaxProfile", minimaxModelID, profile)
	}
}

// Test flow:
//  1. Look up the built-in profile for a table of routed model names: the default qwen model, an empty string, near-miss version strings for kimi, minimax and glm, and a kimi ID with leading whitespace.
//  2. Assert BuiltInProfileFor returns nil for every case.
func TestBuiltInProfileForUnknownModelReturnsNil(t *testing.T) {
	tests := []struct {
		name        string
		routedModel string
	}{
		{"qwen uses the default profile", "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"},
		{"empty routed model", ""},
		{"near-miss kimi version does not match", "moonshotai/Kimi-K2.5"},
		{"near-miss minimax version does not match", "MiniMaxAI/MiniMax-M2"},
		{"near-miss glm variant does not match", "zai-org/GLM-5.3"},
		{"kimi id with leading whitespace does not match", " " + kimiModelID},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if profile := BuiltInProfileFor(testCase.routedModel); profile != nil {
				t.Errorf("BuiltInProfileFor(%q) = %v, want nil", testCase.routedModel, profile)
			}
		})
	}
}

// Test flow:
//  1. List the built-in models and look each one's preset up.
//  2. Assert the three bound models are listed in order with their presets, and an unbound model has none.
func TestBuiltInBindingsByPreset(t *testing.T) {
	want := map[string]PresetName{glm53FlashModelID: presetGLMFlashForTest, kimiModelID: presetKimiForTest, minimaxModelID: presetMinimaxForTest}
	models := BuiltInModels()
	if len(models) != len(want) || models[0] != minimaxModelID || models[1] != kimiModelID || models[2] != glm53FlashModelID {
		t.Fatalf("BuiltInModels() = %v, want the three bound models in byte order", models)
	}
	for model, preset := range want {
		if got, bound := BuiltInPresetFor(model); !bound || got != preset {
			t.Errorf("BuiltInPresetFor(%q) = %q, %v; want %q", model, got, bound, preset)
		}
	}
	if _, bound := BuiltInPresetFor("Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"); bound {
		t.Error("Qwen has a built-in preset")
	}
}

// Test flow:
//  1. Check model names the chat path would accept and ones it would refuse.
//  2. Assert IsValidModelName agrees with the request rule.
func TestIsValidModelName(t *testing.T) {
	for _, valid := range []string{kimiModelID, "qwen", "a.b_c-d/e"} {
		if !IsValidModelName(valid) {
			t.Errorf("IsValidModelName(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"", " ", "bad model", strings.Repeat("a", modelMaxLen+1), "model?"} {
		if IsValidModelName(invalid) {
			t.Errorf("IsValidModelName(%q) = true", invalid)
		}
	}
}

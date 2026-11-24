package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/user"
)

// Test flow:
//  1. Bind the harness model to the minimax preset through the model-filter settings.
//  2. POST a chat request that enables thinking.
//  3. Assert the body raced to the hosts no longer carries thinking: the chat path normalised it with the stored setting.
func TestChatUsesStoredModelFilters(t *testing.T) {
	live := newHarness(t)
	if err := live.filters.Put(context.Background(), "qwen", filters.ModelSetting{Preset: presetMinimaxForTest}); err != nil {
		t.Fatalf("Put(): %v", err)
	}

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"qwen","messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled"}}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	prompt := string(live.inference.raced.Load().Params.(user.InferenceParams).Prompt)
	if strings.Contains(prompt, `"thinking"`) {
		t.Errorf("raced body kept thinking: %s", prompt)
	}
}

// Test flow:
//  1. Bind the harness model to the kimi preset, then race a chat request; race another after binding it to default.
//  2. Assert the engine request carries the empty-answer flag only under kimi: the engine classifies by what the chat path hands it.
func TestChatPassesEmptyAnswerFlagToEngine(t *testing.T) {
	live := newHarness(t)
	for preset, want := range map[filters.PresetName]bool{presetKimiForTest: true, filters.PresetDefault: false} {
		if err := live.filters.Put(context.Background(), "qwen", filters.ModelSetting{Preset: preset}); err != nil {
			t.Fatalf("Put(%s): %v", preset, err)
		}

		recorder := live.request(t, http.MethodPost, "/v1/chat/completions", `{"model":"qwen","messages":[{"role":"user","content":"x"}]}`, nil)

		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", preset, recorder.Code, recorder.Body.String())
		}
		if got := live.inference.raced.Load().EmptyAnswerCountsTokens; got != want {
			t.Errorf("%s: EmptyAnswerCountsTokens = %v, want %v", preset, got, want)
		}
	}
}

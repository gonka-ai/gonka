package probe

import (
	"reflect"
	"testing"

	"devshard/user"
)

// Test flow:
//  1. Take the probe prompt the three call sites formatted before they shared this package, with its `max_tokens` written as 1.
//  2. Call `Params` with a fixed model, a fixed start in seconds and that prompt.
//  3. Assert `MaxTokens` is 1 and the params carry the model, the prompt, its fixed length of 59 bytes as the input length, a reservation of 1 token and the start, with every other field zero.
func TestParamsKeepTheBudgetTheCallSitesUsed(t *testing.T) {
	prompt := []byte(`{"messages":[{"role":"user","content":"."}],"max_tokens":1}`)

	params := Params("qwen", 1_700_000_000, prompt)

	if MaxTokens != 1 {
		t.Errorf("MaxTokens = %d, want 1", MaxTokens)
	}
	want := user.InferenceParams{
		Model:       "qwen",
		Prompt:      prompt,
		InputLength: 59,
		MaxTokens:   1,
		StartedAt:   1_700_000_000,
	}
	if !reflect.DeepEqual(params, want) {
		t.Errorf("Params = %+v, want %+v", params, want)
	}
}

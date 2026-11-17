package filters

import (
	"errors"
	"strings"
	"testing"
)

func templateKwargs(t *testing.T, document *Document) map[string]any {
	t.Helper()
	kwargs, present, isObject := document.ObjectField("chat_template_kwargs")
	if !present || !isObject {
		t.Fatalf("chat_template_kwargs missing")
	}
	return kwargs
}

// Test flow:
//  1. Bind Qwen to a policy forcing clear_thinking=false and preserve_thinking=true.
//  2. Send a request whose kwargs set clear_thinking=true.
//  3. Assert both forced values win.
func TestForcedKwargsOverwriteClient(t *testing.T) {
	knobs := FilterKnobs{TemplateKwargs: &TemplateKwargsPolicy{Forced: map[string]any{"clear_thinking": false, "preserve_thinking": true}}}

	document, err := normalizeWithKnobs(t, knobs, `"chat_template_kwargs":{"clear_thinking":true}`)

	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	kwargs := templateKwargs(t, document)
	if kwargs["clear_thinking"] != false || kwargs["preserve_thinking"] != true {
		t.Errorf("kwargs = %v, want the forced values", kwargs)
	}
}

// Test flow:
//  1. Bind Qwen to a policy forcing one key, with a request that carries no kwargs.
//  2. Assert the kwargs object is created with the forced key.
func TestForcedKwargsCreatesObjectWhenAbsent(t *testing.T) {
	document, err := normalizeWithKnobs(t, FilterKnobs{TemplateKwargs: &TemplateKwargsPolicy{Forced: map[string]any{"mode": "fast"}}}, `"stream":false`)
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if kwargs := templateKwargs(t, document); kwargs["mode"] != "fast" {
		t.Errorf("kwargs = %v, want mode=fast", kwargs)
	}
}

// Test flow:
//  1. Bind Qwen to a policy denying drop_thinking and enable_thinking.
//  2. Send drop_thinking in kwargs and enable_thinking at the top level, which the gateway mirrors into kwargs.
//  3. Assert the request passes and neither key reaches the host.
func TestDeniedKwargsRemoved(t *testing.T) {
	knobs := FilterKnobs{TemplateKwargs: &TemplateKwargsPolicy{Denied: []string{"drop_thinking", "enable_thinking"}}}

	document, err := normalizeWithKnobs(t, knobs, `"enable_thinking":true,"chat_template_kwargs":{"drop_thinking":true,"keep":1}`)

	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	kwargs := templateKwargs(t, document)
	if _, kept := kwargs["drop_thinking"]; kept {
		t.Error("drop_thinking reached the host")
	}
	if _, kept := kwargs["enable_thinking"]; kept {
		t.Error("enable_thinking reached the host")
	}
	if _, kept := kwargs["keep"]; !kept {
		t.Error("an allowed key was dropped")
	}
}

// Test flow:
//  1. Compose policies with a globally forbidden key, an object value, an over-long string, an empty key, and a key both forced and denied.
//  2. Assert each is refused as an invalid setting.
func TestComposeRefusesUnsafeForcedKwargs(t *testing.T) {
	cases := map[string]TemplateKwargsPolicy{
		"forced_chat_template": {Forced: map[string]any{"chat_template": "x"}},
		"denied_tokenize":      {Denied: []string{"tokenize"}},
		"object_value":         {Forced: map[string]any{"options": map[string]any{"a": 1}}},
		"long_string":          {Forced: map[string]any{"mode": strings.Repeat("x", 300)}},
		"empty_key":            {Forced: map[string]any{"": true}},
		"forced_and_denied":    {Forced: map[string]any{"mode": true}, Denied: []string{"mode"}},
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ComposeProfile(presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{TemplateKwargs: &policy}}); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

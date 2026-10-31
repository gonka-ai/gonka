package filters

import (
	"errors"
	"reflect"
	"testing"
	"testing/fstest"
)

const presetsTestQwenModel = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

func expectedProfile(adjust func(*Profile)) Profile {
	profile := Profile{
		ThinkingTemplateKey: enableThinkingKwargKey, ReasoningEffort: ReasoningEffortPolicy{Mode: ReasoningEffortForward},
		Tools: ToolsAllow, ParallelToolCalls: ParallelToolCallsKeep,
	}
	adjust(&profile)
	return profile
}

func composeForTest(t *testing.T, model string, setting ModelSetting) *Profile {
	t.Helper()
	profile, err := ComposeProfile(model, setting)
	if err != nil {
		t.Fatalf("ComposeProfile(%q, %+v) error = %v", model, setting, err)
	}
	return profile
}

// Test flow:
//  1. Compose every preset on the model its built-in binding serves.
//  2. Assert each declares exactly its model's deltas over the default, field for field, and default declares none.
func TestPresetsDeclareTheirModelDeltas(t *testing.T) {
	cases := []struct {
		preset PresetName
		model  string
		want   Profile
	}{
		{presetKimiForTest, kimiModelID, expectedProfile(func(profile *Profile) {
			profile.ForceZeroPenalties, profile.RejectStructuredOutputs, profile.AllowSafetyIdentifier = true, true, true
			profile.Thinking, profile.ThinkingTemplateKey = ThinkingMirrorToKwargs, "thinking"
			profile.ThinkingTokenBudget = &ThinkingBudget{ForceZeroBelow: 256, DefaultDivisor: 2, AbsoluteMax: 96_000, ContentHeadroom: 64}
			profile.EmptyAnswerCountsTokens, profile.OutputTokensFloor = true, 16
		})},
		{presetMinimaxForTest, minimaxModelID, expectedProfile(func(profile *Profile) {
			profile.Thinking, profile.KeepReasoningSplit = ThinkingStrip, true
		})},
		{presetGLMFlashForTest, glm53FlashModelID, expectedProfile(func(profile *Profile) { profile.Thinking = ThinkingForceOn })},
		{PresetDefault, presetsTestQwenModel, expectedProfile(func(*Profile) {})},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.preset), func(t *testing.T) {
			if got := *composeForTest(t, testCase.model, ModelSetting{Preset: testCase.preset}); !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("ComposeProfile(%s) = %+v, want %+v", testCase.preset, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Compose glm_flash with an output-token floor and forced zero penalties on top.
//  2. Assert the knobs win and the preset's thinking disposition survives.
func TestKnobOverridesPreset(t *testing.T) {
	floor := uint64(32)
	forceZero := true
	profile := composeForTest(t, presetsTestQwenModel, ModelSetting{
		Preset:  presetGLMFlashForTest,
		Filters: FilterKnobs{OutputTokensFloor: &floor, ForceZeroPenalties: &forceZero},
	})
	want := expectedProfile(func(profile *Profile) {
		profile.Thinking, profile.ForceZeroPenalties, profile.OutputTokensFloor = ThinkingForceOn, true, 32
	})
	if !reflect.DeepEqual(*profile, want) {
		t.Errorf("profile = %+v, want %+v", *profile, want)
	}
}

// Test flow:
//  1. Compose with no preset and only a thinking knob.
//  2. Assert the base is default with the knob applied.
func TestKnobsWithoutPresetStartFromDefault(t *testing.T) {
	strip := ThinkingStrip
	profile := composeForTest(t, presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{Thinking: &strip}})
	if !reflect.DeepEqual(*profile, expectedProfile(func(profile *Profile) { profile.Thinking = ThinkingStrip })) {
		t.Errorf("profile = %+v, want only Thinking=Strip", *profile)
	}
}

// Test flow:
//  1. Compose settings the gateway must refuse: an unknown preset, a floor above the ceiling, budgets with impossible numbers, and a forbidden template key.
//  2. Assert each is refused as an invalid model setting.
func TestComposeRefusesInvalidSettings(t *testing.T) {
	tooHigh := MaxOutputTokensFloor + 1
	cases := []struct {
		name    string
		model   string
		setting ModelSetting
	}{
		{"unknown_preset", presetsTestQwenModel, ModelSetting{Preset: "retired"}},
		{"floor_above_ceiling", presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{OutputTokensFloor: &tooHigh}}},
		{"budget_divisor_zero", presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{ThinkingTokenBudget: &ThinkingBudget{AbsoluteMax: 100}}}},
		{"budget_above_output_ceiling", presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{ThinkingTokenBudget: &ThinkingBudget{DefaultDivisor: 2, AbsoluteMax: MaxOutputTokens + 1}}}},
		{"budget_headroom_above_floor_ceiling", presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{ThinkingTokenBudget: &ThinkingBudget{DefaultDivisor: 2, ContentHeadroom: MaxOutputTokensFloor + 1}}}},
		{"template_key_forbidden", presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{ThinkingTemplateKey: stringPointer("chat_template")}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ComposeProfile(testCase.model, testCase.setting); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

// Test flow:
//  1. Compose the same preset twice.
//  2. Assert each call returns its own profile, so a stored setting never aliases the catalog.
func TestComposeReturnsAFreshProfile(t *testing.T) {
	first := composeForTest(t, kimiModelID, ModelSetting{Preset: presetKimiForTest})
	second := composeForTest(t, kimiModelID, ModelSetting{Preset: presetKimiForTest})
	if first == second || first == kimiProfile {
		t.Error("ComposeProfile returned a shared profile")
	}
}

// Test flow:
//  1. Marshal every thinking disposition as text and parse it back; parse an unknown word.
//  2. Assert the round trip is exact and the unknown word is refused.
func TestThinkingDispositionTextRoundTrip(t *testing.T) {
	wire := map[ThinkingDisposition]string{
		ThinkingNormalizeInPlace: "normalize",
		ThinkingMirrorToKwargs:   "mirror_to_kwargs",
		ThinkingStrip:            "strip",
		ThinkingForceOn:          "force_on",
	}
	for disposition, text := range wire {
		marshalled, err := disposition.MarshalText()
		if err != nil || string(marshalled) != text {
			t.Errorf("MarshalText(%d) = %q, %v; want %q", disposition, marshalled, err, text)
		}
		var parsed ThinkingDisposition
		if err := parsed.UnmarshalText([]byte(text)); err != nil || parsed != disposition {
			t.Errorf("UnmarshalText(%q) = %d, %v; want %d", text, parsed, err, disposition)
		}
	}
	var parsed ThinkingDisposition
	if err := parsed.UnmarshalText([]byte("maybe")); err == nil {
		t.Error("UnmarshalText(maybe) = nil, want an error")
	}
}

// Test flow:
//  1. List the preset names.
//  2. Assert all four are present in sorted order and each resolves in the catalog.
func TestPresetCatalog(t *testing.T) {
	want := []PresetName{PresetDefault, presetGLMFlashForTest, presetKimiForTest, presetMinimaxForTest}
	got := PresetNames()
	if len(got) != len(want) {
		t.Fatalf("PresetNames() = %v, want %v", got, want)
	}
	for index, name := range want {
		if got[index] != name {
			t.Errorf("PresetNames()[%d] = %q, want %q", index, got[index], name)
		}
		if _, ok := PresetCatalog()[name]; !ok {
			t.Errorf("PresetCatalog() lacks %q", name)
		}
	}
}

// Test flow:
//  1. Check an empty setting, a preset-only setting and a knob-only setting.
//  2. Assert only the empty one reports IsEmpty.
func TestModelSettingIsEmpty(t *testing.T) {
	strip := ThinkingStrip
	if !(ModelSetting{}).IsEmpty() {
		t.Error("an empty setting is not empty")
	}
	if (ModelSetting{Preset: presetKimiForTest}).IsEmpty() {
		t.Error("a preset-only setting is empty")
	}
	if (ModelSetting{Filters: FilterKnobs{Thinking: &strip}}).IsEmpty() {
		t.Error("a knob-only setting is empty")
	}
}

// Test flow:
//  1. Load the embedded preset files.
//  2. Assert the four presets load, and every built-in model is bound by exactly one of them.
func TestEveryEmbeddedPresetComposes(t *testing.T) {
	profiles, bindings, err := loadPresets(embeddedPresetFiles)
	if err != nil {
		t.Fatalf("loadPresets(embedded) error = %v", err)
	}
	if len(profiles) != 4 {
		t.Errorf("presets = %d, want 4", len(profiles))
	}
	want := map[string]PresetName{kimiModelID: presetKimiForTest, minimaxModelID: presetMinimaxForTest, glm53FlashModelID: presetGLMFlashForTest}
	if !reflect.DeepEqual(bindings, want) {
		t.Errorf("bindings = %v, want %v", bindings, want)
	}
}

// Test flow:
//  1. Load preset folders with no default, a default that leaves a choice unset, a misspelt knob, an out-of-range floor, and one model bound by two presets.
//  2. Assert each folder is refused rather than loaded half way, and a folder with only a valid default loads.
func TestBrokenPresetFilesAreRefused(t *testing.T) {
	validDefault := &fstest.MapFile{Data: []byte(`{"models":[],"filters":{"thinking_template_key":"enable_thinking","reasoning_effort":{"mode":"forward"},"tools":"allow","parallel_tool_calls":"keep"}}`)}
	cases := map[string]fstest.MapFS{
		"default_missing":               {"presets/a.json": {Data: []byte(`{"models":[],"filters":{}}`)}},
		"default_leaves_a_choice_unset": {"presets/default.json": {Data: []byte(`{"models":[],"filters":{}}`)}},
		"unknown_knob":                  {"presets/default.json": validDefault, "presets/a.json": {Data: []byte(`{"models":[],"filters":{"thinkng":"strip"}}`)}},
		"floor_too_high":                {"presets/default.json": validDefault, "presets/a.json": {Data: []byte(`{"models":[],"filters":{"output_tokens_floor":99999}}`)}},
		"model_bound_twice": {
			"presets/default.json": validDefault,
			"presets/a.json":       {Data: []byte(`{"models":["m/x"],"filters":{}}`)},
			"presets/b.json":       {Data: []byte(`{"models":["m/x"],"filters":{}}`)},
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := loadPresets(files); err == nil {
				t.Error("loadPresets() = nil, want a refusal")
			}
		})
	}
	if _, _, err := loadPresets(fstest.MapFS{"presets/default.json": validDefault}); err != nil {
		t.Errorf("a folder with only a valid default: error = %v, want nil", err)
	}
}

// Test flow:
//  1. Bind Qwen to a profile that mirrors thinking under a custom template key.
//  2. Normalize a request with thinking enabled and assert the flag lands under that key and the top-level field is gone.
func TestThinkingTemplateKeyFollowsProfile(t *testing.T) {
	mirror := ThinkingMirrorToKwargs
	key := "reasoning_on"
	profile := composeForTest(t, presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{Thinking: &mirror, ThinkingTemplateKey: &key}})
	document := normalizedWithProfile(t, presetsTestQwenModel, profile, `"thinking":{"type":"enabled"}`)

	kwargs, _, _ := document.ObjectField("chat_template_kwargs")
	if kwargs["reasoning_on"] != true || document.Has("thinking") {
		t.Errorf("kwargs = %v, thinking kept = %v; want reasoning_on=true and no top-level thinking", kwargs, document.Has("thinking"))
	}
}

// Test flow:
//  1. Give Kimi a budget of {force zero below 100, divisor 4, max 500, headroom 10}.
//  2. Normalize with max_tokens 1000 and 50.
//  3. Assert the first invents 1000/4 = 250, and the second forces 0 and silences thinking under the profile's key.
func TestBudgetNumbersFollowProfile(t *testing.T) {
	budget := ThinkingBudget{ForceZeroBelow: 100, DefaultDivisor: 4, AbsoluteMax: 500, ContentHeadroom: 10}
	profile := composeForTest(t, kimiModelID, ModelSetting{Preset: presetKimiForTest, Filters: FilterKnobs{ThinkingTokenBudget: &budget}})

	roomy := normalizedWithProfile(t, kimiModelID, profile, `"max_tokens":1000`)
	tight := normalizedWithProfile(t, kimiModelID, profile, `"max_tokens":50`)

	if got, _ := roomy.Uint("thinking_token_budget"); got != 250 {
		t.Errorf("budget at max_tokens 1000 = %d, want 250", got)
	}
	if got, _ := tight.Uint("thinking_token_budget"); got != 0 {
		t.Errorf("budget at max_tokens 50 = %d, want 0", got)
	}
	if kwargs, _, _ := tight.ObjectField("chat_template_kwargs"); kwargs["thinking"] != false {
		t.Errorf("kwargs = %v, want thinking=false", kwargs)
	}
}

func normalizedWithProfile(t *testing.T, model string, profile *Profile, extraFields string) *Document {
	t.Helper()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"x"}],` + extraFields + `}`
	result, err := NormalizeRequest([]byte(body), Options{Profiles: NewProfileSet(map[string]*Profile{model: profile})})
	if err != nil {
		t.Fatalf("NormalizeRequest(%s) error = %v", body, err)
	}
	document, err := ParseDocument(result.Body)
	if err != nil {
		t.Fatalf("ParseDocument() error = %v", err)
	}
	return document
}

func stringPointer(value string) *string { return &value }

// Test flow:
//  1. Compose the kimi preset on Qwen, a model with no built-in budget.
//  2. Assert it composes, and that the result needs a registered model while a plain setting does not.
func TestComposeAcceptsBudgetOnAnyModel(t *testing.T) {
	withBudget := composeForTest(t, presetsTestQwenModel, ModelSetting{Preset: presetKimiForTest})
	withoutBudget := composeForTest(t, presetsTestQwenModel, ModelSetting{Preset: presetMinimaxForTest})
	exemptionOnly := composeForTest(t, presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{EmptyAnswerCountsTokens: boolPointer(true)}})

	if withBudget.ThinkingTokenBudget == nil || !withBudget.RequiresRegisteredModel() {
		t.Error("the kimi preset on Qwen lost its budget or does not require a registered model")
	}
	if withoutBudget.RequiresRegisteredModel() {
		t.Error("a setting without budget or exemption requires a registered model")
	}
	if !exemptionOnly.RequiresRegisteredModel() {
		t.Error("the empty-answer exemption alone does not require a registered model")
	}
}

func boolPointer(value bool) *bool { return &value }

// Test flow:
//  1. Write out the knobs of a model with no profile at all, and the knobs of the default preset.
//  2. Assert they match, so an admin answer for an unconfigured model shows what its requests actually run.
func TestUnconfiguredModelRunsTheDefaultPreset(t *testing.T) {
	var unconfigured *Profile
	want := PresetCatalog()[PresetDefault]
	if got := unconfigured.Knobs(); !reflect.DeepEqual(got, want) {
		t.Errorf("Knobs(nil) = %+v, want %+v", got, want)
	}
}

// Test flow:
//  1. Compose a budget together with each knob that would undo its silence: thinking forced on, the template key denied, the template key forced.
//  2. Assert each is refused: below the force-zero threshold the template must see thinking off.
func TestComposeRefusesKnobsThatUndoTheBudgetSilence(t *testing.T) {
	forceOn := ThinkingForceOn
	cases := map[string]ModelSetting{
		"force_on_with_budget": {Filters: FilterKnobs{Thinking: &forceOn, ThinkingTokenBudget: &ThinkingBudget{ForceZeroBelow: 256, DefaultDivisor: 2, AbsoluteMax: 1000}}},
		"template_key_denied":  {Preset: presetKimiForTest, Filters: FilterKnobs{TemplateKwargs: &TemplateKwargsPolicy{Denied: []string{"thinking"}}}},
		"template_key_forced":  {Preset: presetKimiForTest, Filters: FilterKnobs{TemplateKwargs: &TemplateKwargsPolicy{Forced: map[string]any{"thinking": true}}}},
	}
	for name, setting := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ComposeProfile(presetsTestQwenModel, setting); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

// Test flow:
//  1. Compose the kimi preset, change the composed profile's budget, and compose it again; then change a catalog copy's budget and read the catalog again.
//  2. Assert neither change reaches the catalog: every caller gets its own copy.
func TestComposedProfilesAndCatalogCopiesShareNothing(t *testing.T) {
	first := composeForTest(t, kimiModelID, ModelSetting{Preset: presetKimiForTest})
	first.ThinkingTokenBudget.AbsoluteMax = 1
	second := composeForTest(t, kimiModelID, ModelSetting{Preset: presetKimiForTest})
	catalogCopy := PresetCatalog()[presetKimiForTest]
	catalogCopy.ThinkingTokenBudget.AbsoluteMax = 2
	again := PresetCatalog()[presetKimiForTest]

	if second.ThinkingTokenBudget.AbsoluteMax != 96_000 {
		t.Errorf("a change to one composed profile reached the next: absolute_max = %d", second.ThinkingTokenBudget.AbsoluteMax)
	}
	if again.ThinkingTokenBudget.AbsoluteMax != 96_000 {
		t.Errorf("a change to a catalog copy reached the catalog: absolute_max = %d", again.ThinkingTokenBudget.AbsoluteMax)
	}
}

// Test flow:
//  1. Give Kimi a budget whose absolute max is 0, then one whose content headroom is above max_tokens, and send max_tokens at or above force_zero_below with thinking on in the kwargs.
//  2. Assert both resolve to a budget of 0 and silence thinking in the template, as the force-zero branch does.
//  3. Send a client's own budget of 0 under Kimi's numbers and assert its kwargs are left as the client wrote them.
func TestBudgetClampedToZeroSilencesThinking(t *testing.T) {
	cases := map[string]struct {
		budget    ThinkingBudget
		maxTokens string
	}{
		"absolute_max_zero":         {ThinkingBudget{ForceZeroBelow: 256, DefaultDivisor: 2, AbsoluteMax: 0, ContentHeadroom: 64}, "1000"},
		"headroom_above_max_tokens": {ThinkingBudget{ForceZeroBelow: 256, DefaultDivisor: 2, AbsoluteMax: 96_000, ContentHeadroom: 600}, "500"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			profile := composeForTest(t, kimiModelID, ModelSetting{Preset: presetKimiForTest, Filters: FilterKnobs{ThinkingTokenBudget: &testCase.budget}})
			document := normalizedWithProfile(t, kimiModelID, profile, `"max_tokens":`+testCase.maxTokens+`,"chat_template_kwargs":{"thinking":true}`)
			if got, _ := document.Uint("thinking_token_budget"); got != 0 {
				t.Errorf("budget = %d, want 0", got)
			}
			if kwargs := templateKwargs(t, document); kwargs["thinking"] != false {
				t.Errorf("kwargs = %v, want thinking=false", kwargs)
			}
		})
	}
	clientZero := normalizedWithProfile(t, kimiModelID, kimiProfile, `"max_tokens":1000,"thinking_token_budget":0,"chat_template_kwargs":{"thinking":true}`)
	if kwargs := templateKwargs(t, clientZero); kwargs["thinking"] != true {
		t.Errorf("client's own zero budget: kwargs = %v, want thinking kept true", kwargs)
	}
}

// Test flow:
//  1. Compose the glm_flash preset, which forces thinking on, with its template key denied, then with it forced off.
//  2. Assert both are refused: the kwargs policy would undo the force-on it runs after.
func TestComposeRefusesKwargsThatUndoForceOn(t *testing.T) {
	cases := map[string]TemplateKwargsPolicy{
		"denied": {Denied: []string{enableThinkingKwargKey}},
		"forced": {Forced: map[string]any{enableThinkingKwargKey: false}},
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ComposeProfile(presetsTestQwenModel, ModelSetting{Preset: presetGLMFlashForTest, Filters: FilterKnobs{TemplateKwargs: &policy}}); !errors.Is(err, ErrInvalidModelSetting) {
				t.Errorf("ComposeProfile() error = %v, want ErrInvalidModelSetting", err)
			}
		})
	}
}

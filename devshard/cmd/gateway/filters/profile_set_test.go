package filters

import "testing"

// Test flow:
//  1. Resolve every built-in model, DeepSeek and Qwen through a nil profile set.
//  2. Assert the built-in bindings apply and the others get the default preset.
func TestNilProfileSetUsesBuiltInBindings(t *testing.T) {
	var set *ProfileSet
	cases := map[string]*Profile{
		kimiModelID:          kimiProfile,
		minimaxModelID:       minimaxProfile,
		glm53FlashModelID:    glm53FlashProfile,
		deepseekModelID:      defaultProfile,
		presetsTestQwenModel: defaultProfile,
	}
	for model, want := range cases {
		if got := set.For(model); got != want {
			t.Errorf("For(%q) = %v, want %v", model, got, want)
		}
	}
}

// Test flow:
//  1. Build a set that binds Kimi to a composed default profile.
//  2. Assert Kimi loses its thinking budget while MiniMax keeps its built-in binding.
func TestStoredSettingOverridesBuiltIn(t *testing.T) {
	set := NewProfileSet(map[string]*Profile{kimiModelID: composeForTest(t, kimiModelID, ModelSetting{Preset: PresetDefault})})
	if set.For(kimiModelID).thinkingBudget() != nil {
		t.Error("Kimi kept its thinking budget after being bound to default")
	}
	if set.For(minimaxModelID) != minimaxProfile {
		t.Error("MiniMax lost its built-in binding")
	}
}

// Test flow:
//  1. Build a set from a map, then change the map.
//  2. Assert the set kept its own copy.
func TestProfileSetCopiesItsInput(t *testing.T) {
	byModel := map[string]*Profile{presetsTestQwenModel: composeForTest(t, presetsTestQwenModel, ModelSetting{Preset: presetMinimaxForTest})}
	set := NewProfileSet(byModel)
	delete(byModel, presetsTestQwenModel)
	if set.For(presetsTestQwenModel) == nil {
		t.Error("the set followed a change to the map it was built from")
	}
}

// Test flow:
//  1. Normalize a Qwen request with thinking enabled, once without a profile set and once with a set binding Qwen to minimax.
//  2. Assert only the bound run strips the thinking field.
func TestPipelineUsesProfileSet(t *testing.T) {
	body := []byte(`{"model":"` + presetsTestQwenModel + `","messages":[{"role":"user","content":"x"}],"thinking":{"type":"enabled"}}`)
	unbound, err := NormalizeRequest(body, Options{})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	unboundDocument, err := ParseDocument(unbound.Body)
	if err != nil {
		t.Fatalf("ParseDocument() error = %v", err)
	}
	if !unboundDocument.Has("thinking") {
		t.Fatal("the default profile dropped thinking; the test needs a field only minimax strips")
	}
	set := NewProfileSet(map[string]*Profile{presetsTestQwenModel: composeForTest(t, presetsTestQwenModel, ModelSetting{Preset: presetMinimaxForTest})})
	bound, err := NormalizeRequest(body, Options{Profiles: set})
	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	boundDocument, err := ParseDocument(bound.Body)
	if err != nil {
		t.Fatalf("ParseDocument() error = %v", err)
	}
	if boundDocument.Has("thinking") {
		t.Error("a Qwen request bound to minimax kept thinking")
	}
}

// Test flow:
//  1. Bind a model to a profile built over the default preset that forces zero penalties, floors output at 32, keeps safety_identifier, refuses structured_outputs, defaults reasoning_split and mirrors thinking under its own key.
//  2. Normalize requests for that model, and resolve a model nothing binds.
//  3. Assert every rule follows the resolved profile, not behaviour written into the rules, and the unbound model resolves to the default preset itself.
func TestRulesFollowTheResolvedProfile(t *testing.T) {
	mirror := ThinkingMirrorToKwargs
	key := "reasoning_on"
	customDefault, err := composeOver(*defaultProfile, FilterKnobs{
		ForceZeroPenalties: boolPointer(true), OutputTokensFloor: uint64Pointer(32), AllowSafetyIdentifier: boolPointer(true),
		RejectStructuredOutputs: boolPointer(true), KeepReasoningSplit: boolPointer(true), DefaultReasoningSplit: boolPointer(true),
		Thinking: &mirror, ThinkingTemplateKey: &key,
	})
	if err != nil {
		t.Fatalf("composeOver() error = %v", err)
	}
	set := NewProfileSet(map[string]*Profile{"acme/unbound": customDefault})
	normalize := func(fields string) (*Document, error) {
		result, err := NormalizeRequest([]byte(`{"model":"acme/unbound","messages":[{"role":"user","content":"x"}],`+fields+`}`), Options{Profiles: set})
		if err != nil {
			return nil, err
		}
		return ParseDocument(result.Body)
	}

	document, err := normalize(`"frequency_penalty":1,"max_tokens":10,"safety_identifier":"user-1","thinking":{"type":"enabled"}`)
	_, refused := normalize(`"structured_outputs":{"json":{"type":"object"}}`)

	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if value := numberField(t, document, "frequency_penalty"); value != 0 {
		t.Errorf("frequency_penalty = %v, want 0", value)
	}
	if value := numberField(t, document, "max_tokens"); value != 32 {
		t.Errorf("max_tokens = %v, want the default's floor 32", value)
	}
	if !document.Has("safety_identifier") {
		t.Error("safety_identifier was stripped")
	}
	if value, _ := document.Get("reasoning_split"); value != true {
		t.Errorf("reasoning_split = %v, want the default true", value)
	}
	if kwargs := templateKwargs(t, document); kwargs["reasoning_on"] != true {
		t.Errorf("kwargs = %v, want reasoning_on=true", kwargs)
	}
	if ErrorStatus(refused, 0) != 400 {
		t.Errorf("structured_outputs: error = %v, want a 400", refused)
	}
	if set.For("acme/nothing-binds-this") != defaultProfile {
		t.Error("an unbound model does not resolve to the default preset")
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }

package filters

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Test flow:
//  1. Apply `reasoningWrapper()` to a document holding `{"reasoning":{"effort":"high"}}`.
//  2. Assert the `reasoning` wrapper is deleted.
//  3. Assert `reasoning_effort` is lifted to "high".
func TestReasoningWrapperLiftsEffort(t *testing.T) {
	document := parseTestDocument(t, `{"reasoning":{"effort":"high"}}`)
	if err := reasoningWrapper()(RuleContext{Document: document}); err != nil {
		t.Fatalf("reasoningWrapper() = %v, want nil", err)
	}
	if document.Has("reasoning") {
		t.Error("reasoning wrapper must be deleted")
	}
	if got, _ := document.Get("reasoning_effort"); got != "high" {
		t.Errorf("reasoning_effort = %v, want %q", got, "high")
	}
}

// Test flow:
//  1. Apply `reasoningWrapper()` to a document whose `reasoning` wrapper sets `enabled:false` alongside an `effort`.
//  2. Assert the wrapper is dropped.
//  3. Assert `reasoning_effort` is recorded as "none" rather than silently vanishing, since a dropped wrapper would otherwise be indistinguishable from no request at all.
func TestReasoningWrapperEnabledFalseRecordsTheRefusal(t *testing.T) {
	document := parseTestDocument(t, `{"reasoning":{"enabled":false,"effort":"high"}}`)
	if err := reasoningWrapper()(RuleContext{Document: document}); err != nil {
		t.Fatalf("reasoningWrapper() = %v, want nil", err)
	}
	if document.Has("reasoning") {
		t.Error("the wrapper must be dropped")
	}
	if got, _ := document.Get("reasoning_effort"); got != "none" {
		t.Errorf("reasoning_effort = %v, want %q: a dropped wrapper is indistinguishable from silence, which a defaulting route fills in", got, "none")
	}
}

// Test flow:
//  1. Apply `reasoningWrapper()` to a document with both a top-level `reasoning_effort` and a `reasoning` wrapper.
//  2. Assert the pre-existing top-level value wins over the wrapper's value.
func TestReasoningWrapperExistingTopLevelWins(t *testing.T) {
	document := parseTestDocument(t, `{"reasoning_effort":"medium","reasoning":{"effort":"high"}}`)
	if err := reasoningWrapper()(RuleContext{Document: document}); err != nil {
		t.Fatalf("reasoningWrapper() = %v, want nil", err)
	}
	if got, _ := document.Get("reasoning_effort"); got != "medium" {
		t.Errorf("reasoning_effort = %v, want %q (pre-existing value must win)", got, "medium")
	}
}

// Test flow:
//  1. Apply `reasoningWrapper()` to bodies where `reasoning` is a string, number, boolean, or array instead of an object.
//  2. Assert every case drops the `reasoning` field without lifting anything into `reasoning_effort`.
func TestReasoningWrapperSilentlyDropsNonObject(t *testing.T) {
	for _, body := range []string{`{"reasoning":"high"}`, `{"reasoning":42}`, `{"reasoning":true}`, `{"reasoning":[{"effort":"high"}]}`} {
		t.Run(body, func(t *testing.T) {
			document := parseTestDocument(t, body)
			if err := reasoningWrapper()(RuleContext{Document: document}); err != nil {
				t.Fatalf("reasoningWrapper() = %v, want nil", err)
			}
			if document.Has("reasoning") || document.Has("reasoning_effort") {
				t.Error("non-object wrapper must be dropped without lifting anything")
			}
		})
	}
}

// Test flow:
//  1. Apply `reasoningWrapper()` to a document with no `reasoning` field.
//  2. Assert it returns no error.
func TestReasoningWrapperAbsentIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[]}`)
	if err := reasoningWrapper()(RuleContext{Document: document}); err != nil {
		t.Fatalf("reasoningWrapper() = %v, want nil", err)
	}
}

// Test flow:
//  1. Run `reasoningEffortValidate()` once per known effort level ("none" through "max").
//  2. Assert every level is accepted without error.
func TestReasoningEffortValidateAccepts(t *testing.T) {
	for _, value := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		t.Run(value, func(t *testing.T) {
			document := parseTestDocument(t, `{"reasoning_effort":"`+value+`"}`)
			if err := reasoningEffortValidate()(RuleContext{Document: document}); err != nil {
				t.Errorf("reasoningEffortValidate() = %v, want nil", err)
			}
		})
	}
}

// Test flow:
//  1. Run `reasoningEffortValidate()` on a document with no `reasoning_effort` field.
//  2. Assert it returns no error.
func TestReasoningEffortValidateAbsentIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[]}`)
	if err := reasoningEffortValidate()(RuleContext{Document: document}); err != nil {
		t.Fatalf("reasoningEffortValidate() = %v, want nil", err)
	}
}

// Test flow:
//  1. Run `reasoningEffortValidate()` on bodies where `reasoning_effort` is a number, boolean, or object.
//  2. Assert every case is rejected with the invalid-shape error.
func TestReasoningEffortValidateRejectsWrongShape(t *testing.T) {
	for _, body := range []string{`{"reasoning_effort":5}`, `{"reasoning_effort":true}`, `{"reasoning_effort":{"effort":"high"}}`} {
		t.Run(body, func(t *testing.T) {
			document := parseTestDocument(t, body)
			err := reasoningEffortValidate()(RuleContext{Document: document})
			want := "reasoning_effort: invalid shape: must be a string"
			if err == nil || err.Error() != want {
				t.Errorf("reasoningEffortValidate() = %v, want %q", err, want)
			}
		})
	}
}

// Test flow:
//  1. Run `reasoningEffortValidate()` on a `reasoning_effort` value that isn't a known level.
//  2. Assert the rejection's exact message text, which is golden-pinned by `profile_reasoning_effort_invalid_value_rejected`.
//  3. Assert ErrorStatus is 400.
func TestReasoningEffortValidateRejectsUnknownValue(t *testing.T) {
	document := parseTestDocument(t, `{"reasoning_effort":"maximum"}`)
	err := reasoningEffortValidate()(RuleContext{Document: document})
	want := `reasoning_effort: unsupported value: got "maximum"`
	if err == nil || err.Error() != want {
		t.Fatalf("reasoningEffortValidate() = %v, want %q", err, want)
	}
	if got := ErrorStatus(err, 0); got != 400 {
		t.Errorf("ErrorStatus() = %d, want 400", got)
	}
}

// Test flow:
//  1. Apply `enableThinking()` to `{"enable_thinking":true}` under the minimax profile.
//  2. Assert `enable_thinking` is stripped without ever creating `chat_template_kwargs`.
func TestEnableThinkingStripsForThinkingStripProfile(t *testing.T) {
	document := parseTestDocument(t, `{"enable_thinking":true}`)
	if err := enableThinking()(RuleContext{Document: document, Profile: minimaxProfile}); err != nil {
		t.Fatalf("enableThinking() = %v, want nil", err)
	}
	if document.Has("enable_thinking") || document.Has("chat_template_kwargs") {
		t.Error("enable_thinking must be stripped without ever touching chat_template_kwargs")
	}
}

// Test flow:
//  1. For the default (nil) and kimi profiles, and for both true and false values, apply `enableThinking()` to a document with `enable_thinking` set.
//  2. Assert the top-level `enable_thinking` is deleted after a successful mirror.
//  3. Assert `chat_template_kwargs.enable_thinking` is created with the mirrored value.
func TestEnableThinkingMirrorsForDefaultAndKimiProfiles(t *testing.T) {
	for _, profile := range []*Profile{nil, kimiProfile} {
		for _, value := range []bool{true, false} {
			document := parseTestDocument(t, `{"enable_thinking":`+boolLiteral(value)+`}`)
			if err := enableThinking()(RuleContext{Document: document, Profile: profile}); err != nil {
				t.Fatalf("enableThinking() = %v, want nil", err)
			}
			if document.Has("enable_thinking") {
				t.Error("top-level enable_thinking must be deleted after a successful mirror")
			}
			kwargs, ok := document.Object("chat_template_kwargs")
			if !ok {
				t.Fatal("chat_template_kwargs must be created")
			}
			if kwargs["enable_thinking"] != value {
				t.Errorf("chat_template_kwargs.enable_thinking = %v, want %v", kwargs["enable_thinking"], value)
			}
		}
	}
}

// Test flow:
//  1. Apply `enableThinking()` to a document with both a top-level `enable_thinking:true` and an existing `chat_template_kwargs.enable_thinking:false`.
//  2. Assert the pre-existing nested value wins.
func TestEnableThinkingPreservesExistingNestedValue(t *testing.T) {
	document := parseTestDocument(t, `{"enable_thinking":true,"chat_template_kwargs":{"enable_thinking":false}}`)
	if err := enableThinking()(RuleContext{Document: document}); err != nil {
		t.Fatalf("enableThinking() = %v, want nil", err)
	}
	kwargs, _ := document.Object("chat_template_kwargs")
	if kwargs["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs.enable_thinking = %v, want false (pre-existing value must win)", kwargs["enable_thinking"])
	}
}

// Test flow:
//  1. Apply `enableThinking()` to a document with `enable_thinking:true` and an unrelated `chat_template_kwargs.foo` entry.
//  2. Assert the unrelated entry survives.
//  3. Assert `chat_template_kwargs.enable_thinking` is set to true.
func TestEnableThinkingPreservesOtherKwargsEntries(t *testing.T) {
	document := parseTestDocument(t, `{"enable_thinking":true,"chat_template_kwargs":{"foo":"bar"}}`)
	if err := enableThinking()(RuleContext{Document: document}); err != nil {
		t.Fatalf("enableThinking() = %v, want nil", err)
	}
	kwargs, _ := document.Object("chat_template_kwargs")
	if kwargs["foo"] != "bar" {
		t.Errorf("chat_template_kwargs.foo = %v, want %q", kwargs["foo"], "bar")
	}
	if kwargs["enable_thinking"] != true {
		t.Errorf("chat_template_kwargs.enable_thinking = %v, want true", kwargs["enable_thinking"])
	}
}

// Test flow:
//  1. Apply `enableThinking()` to bodies where `enable_thinking` is a string, number, object, or array instead of a boolean.
//  2. Assert every case returns an error.
func TestEnableThinkingRejectsWrongShape(t *testing.T) {
	for _, body := range []string{`{"enable_thinking":"true"}`, `{"enable_thinking":1}`, `{"enable_thinking":{}}`, `{"enable_thinking":[]}`} {
		t.Run(body, func(t *testing.T) {
			document := parseTestDocument(t, body)
			if err := enableThinking()(RuleContext{Document: document}); err == nil {
				t.Error("enableThinking() = nil, want an error")
			}
		})
	}
}

// Test flow:
//  1. Apply `enableThinking()` to a document with `enable_thinking:true` and a non-object `chat_template_kwargs`.
//  2. Assert it returns an error.
//  3. Assert the top-level `enable_thinking` field is preserved, since it could not be translated safely.
func TestEnableThinkingRejectsWrongShapeKwargsAndPreservesField(t *testing.T) {
	document := parseTestDocument(t, `{"enable_thinking":true,"chat_template_kwargs":"broken"}`)
	err := enableThinking()(RuleContext{Document: document})
	if err == nil {
		t.Fatal("enableThinking() = nil, want an error")
	}
	if !document.Has("enable_thinking") {
		t.Error("top-level field must be preserved when it cannot be translated safely")
	}
}

// Test flow:
//  1. Apply `enableThinking()` to a document with no `enable_thinking` field.
//  2. Assert it returns no error and does not create `chat_template_kwargs`.
func TestEnableThinkingAbsentIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[]}`)
	if err := enableThinking()(RuleContext{Document: document}); err != nil {
		t.Fatalf("enableThinking() = %v, want nil", err)
	}
	if document.Has("chat_template_kwargs") {
		t.Error("chat_template_kwargs must not be created when enable_thinking is absent")
	}
}

// Test flow:
//  1. Apply `thinking()` to `{"thinking":{"type":"enabled"}}` under the minimax profile.
//  2. Assert `thinking` is stripped.
func TestThinkingStripsForThinkingStripProfile(t *testing.T) {
	document := parseTestDocument(t, `{"thinking":{"type":"enabled"}}`)
	if err := thinking()(RuleContext{Document: document, Profile: minimaxProfile}); err != nil {
		t.Fatalf("thinking() = %v, want nil", err)
	}
	if document.Has("thinking") {
		t.Error("thinking must be stripped for a ThinkingStrip profile")
	}
}

// Test flow:
//  1. Run table cases of `thinking()` under the default profile, varying the wrapper's `type` (adaptive, auto, enabled, disabled) alongside a `display` hint.
//  2. Assert the wrapper survives with its type normalized to the expected value.
//  3. Assert the `display` hint is dropped.
func TestThinkingNormalizesTypeForDefaultProfile(t *testing.T) {
	tests := []struct {
		name     string
		typeIn   string
		wantType string
	}{
		{"adaptive normalizes to enabled", "adaptive", "enabled"},
		{"auto normalizes to enabled", "auto", "enabled"},
		{"enabled stays enabled", "enabled", "enabled"},
		{"disabled stays disabled", "disabled", "disabled"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, `{"thinking":{"type":"`+testCase.typeIn+`","display":"summarized"}}`)
			if err := thinking()(RuleContext{Document: document}); err != nil {
				t.Fatalf("thinking() = %v, want nil", err)
			}
			wrapper, ok := document.Object("thinking")
			if !ok {
				t.Fatal("thinking wrapper must survive for the default profile")
			}
			if wrapper["type"] != testCase.wantType {
				t.Errorf("thinking.type = %v, want %q", wrapper["type"], testCase.wantType)
			}
			if _, hasDisplay := wrapper["display"]; hasDisplay {
				t.Error("display hint must be dropped")
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `thinking()` under the kimi profile, varying the wrapper's `type` and the expected mirrored boolean.
//  2. Assert the top-level `thinking` wrapper is dropped after the mirror.
//  3. Assert `chat_template_kwargs.thinking` is created with the expected boolean.
func TestThinkingMirrorsForThinkingMirrorToKwargsProfile(t *testing.T) {
	tests := []struct {
		name        string
		typeIn      string
		wantEnabled bool
	}{
		{"enabled mirrors true", "enabled", true},
		{"adaptive mirrors true", "adaptive", true},
		{"auto mirrors true", "auto", true},
		{"disabled mirrors false", "disabled", false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, `{"thinking":{"type":"`+testCase.typeIn+`"}}`)
			if err := thinking()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
				t.Fatalf("thinking() = %v, want nil", err)
			}
			if document.Has("thinking") {
				t.Error("top-level thinking must be dropped after a mirror")
			}
			kwargs, ok := document.Object("chat_template_kwargs")
			if !ok {
				t.Fatal("chat_template_kwargs must be created")
			}
			if kwargs["thinking"] != testCase.wantEnabled {
				t.Errorf("chat_template_kwargs.thinking = %v, want %v", kwargs["thinking"], testCase.wantEnabled)
			}
		})
	}
}

// Test flow:
//  1. Apply `thinking()` under the kimi profile to a document with both a `thinking.type:enabled` wrapper and an existing `chat_template_kwargs.thinking:false`.
//  2. Assert the pre-existing nested value wins.
func TestThinkingMirrorPreservesExistingNestedValue(t *testing.T) {
	document := parseTestDocument(t, `{"thinking":{"type":"enabled"},"chat_template_kwargs":{"thinking":false}}`)
	if err := thinking()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinking() = %v, want nil", err)
	}
	kwargs, _ := document.Object("chat_template_kwargs")
	if kwargs["thinking"] != false {
		t.Errorf("chat_template_kwargs.thinking = %v, want false (pre-existing value must win)", kwargs["thinking"])
	}
}

// Test flow:
//  1. Run table cases of `thinking()` against a non-object wrapper, an array wrapper, a missing type, a boolean type, and an unknown type string.
//  2. Assert every case returns an error.
func TestThinkingRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"wrapper not object", `{"thinking":"enabled"}`},
		{"wrapper is array", `{"thinking":[]}`},
		{"type missing", `{"thinking":{}}`},
		{"type is bool", `{"thinking":{"type":true}}`},
		{"type is unknown string", `{"thinking":{"type":"on"}}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document := parseTestDocument(t, testCase.body)
			if err := thinking()(RuleContext{Document: document}); err == nil {
				t.Error("thinking() = nil, want an error")
			}
		})
	}
}

// Test flow:
//  1. Apply `thinking()` to a document with no `thinking` field.
//  2. Assert it returns no error.
func TestThinkingAbsentIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{"messages":[]}`)
	if err := thinking()(RuleContext{Document: document}); err != nil {
		t.Fatalf("thinking() = %v, want nil", err)
	}
}

// Test flow:
//  1. Call NormalizeRequest for Kimi, whose profile keeps the budget, with a string-typed `thinking_token_budget`.
//  2. Assert normalization rejects the request: a budget the profile keeps is validated rather than forwarded as sent.
func TestThinkingTokenBudgetRejectsANonNumericValue(t *testing.T) {
	body := `{"model":"moonshotai/Kimi-K2.6","messages":[{"role":"user","content":"x"}],"thinking_token_budget":"lots"}`
	if _, err := NormalizeRequest([]byte(body), Options{RoutedModel: kimiModelID}); err == nil {
		t.Error("NormalizeRequest() = nil, want a rejection of a budget Kimi keeps")
	}
}

// Test flow:
//  1. For both the default (nil) and minimax profiles, apply `thinkingTokenBudgetResolve()` to a document that still carries a budget, small and oversized.
//  2. Assert the field is removed: a profile without a budget never forwards one, even if an earlier stage let it through.
func TestThinkingTokenBudgetResolveRemovesTheFieldWithoutABudget(t *testing.T) {
	for _, profile := range []*Profile{nil, minimaxProfile} {
		for _, body := range []string{`{"max_tokens":4096,"thinking_token_budget":50}`, `{"max_tokens":300,"thinking_token_budget":9000}`} {
			document := parseTestDocument(t, body)
			if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: profile}); err != nil {
				t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
			}
			if document.Has("thinking_token_budget") {
				t.Errorf("thinking_token_budget kept for profile %v and body %s", profile, body)
			}
		}
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the default (nil) profile to a document with `max_tokens` and no `thinking_token_budget`.
//  2. Assert `thinking_token_budget` stays absent, since a profile without a resolution must not gain one — nothing is invented when the caller asked for nothing.
func TestThinkingTokenBudgetResolveInventsNoBudgetWithoutHook(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":4096}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: nil}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, present := document.Get("thinking_token_budget"); present {
		t.Errorf("thinking_token_budget = %v, want absent", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document with `max_tokens` and no `thinking_token_budget`.
//  2. Assert the budget defaults to half of max_tokens.
func TestThinkingTokenBudgetResolveDefaultsToHalf(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":4096}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(2048) {
		t.Errorf("thinking_token_budget = %v, want 2048", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document whose `max_tokens` is below the force-zero threshold, with a client-supplied budget.
//  2. Assert the budget is forced to 0, overriding the client's value.
func TestThinkingTokenBudgetResolveForcesZeroBelowThreshold(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":100,"thinking_token_budget":50}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(0) {
		t.Errorf("thinking_token_budget = %v, want 0 (client value overridden)", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document below the force-zero threshold whose `chat_template_kwargs` already asks to think.
//  2. Assert `chat_template_kwargs.thinking` is overwritten to false while `enable_thinking` is left untouched.
func TestThinkingTokenBudgetResolveForceZeroOverwritesCallerThinking(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":100,"chat_template_kwargs":{"thinking":true,"enable_thinking":true}}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	kwargs, _ := document.Get("chat_template_kwargs")
	want := map[string]any{"thinking": false, "enable_thinking": true}
	if !reflect.DeepEqual(kwargs, want) {
		t.Errorf("chat_template_kwargs = %v, want %v", kwargs, want)
	}
}

// Test flow:
//  1. Call NormalizeRequest for the kimi model with a below-threshold `max_tokens`, once each for a top-level `thinking` wrapper, a direct `chat_template_kwargs.thinking`, an adaptive `thinking` type, and no thinking request at all.
//  2. Assert every entry point ends with `chat_template_kwargs.thinking` forced to false: the thinking rule already mirrors the caller's answer into the kwargs during PreValidation, so by the time the budget is forced to zero at PostLimits the key already exists, and a fill-only write there would leave the template thinking with no budget to think in.
//  3. Assert `thinking_token_budget` is 0 in every case.
func TestNormalizeRequestKimiForceZeroSilencesThinkingThroughEveryEntryPoint(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"top-level thinking", `{"messages":[{"role":"user","content":"x"}],"max_tokens":100,"thinking":{"type":"enabled"}}`},
		{"template kwargs directly", `{"messages":[{"role":"user","content":"x"}],"max_tokens":100,"chat_template_kwargs":{"thinking":true}}`},
		{"adaptive from the CLI", `{"messages":[{"role":"user","content":"x"}],"max_tokens":100,"thinking":{"type":"adaptive"}}`},
		{"nothing asked at all", `{"messages":[{"role":"user","content":"x"}],"max_tokens":100}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := NormalizeRequest([]byte(testCase.body), Options{RoutedModel: kimiModelID})
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want nil", err)
			}
			var document map[string]any
			if err := json.Unmarshal(result.Body, &document); err != nil {
				t.Fatalf("unmarshal normalized body: %v", err)
			}
			kwargs, ok := document["chat_template_kwargs"].(map[string]any)
			if !ok {
				t.Fatalf("chat_template_kwargs missing from %s", result.Body)
			}
			if kwargs["thinking"] != false {
				t.Errorf("chat_template_kwargs.thinking = %v, want false: a zero budget with a thinking template is the burn being fixed", kwargs["thinking"])
			}
			if budget := document["thinking_token_budget"]; budget != float64(0) {
				t.Errorf("thinking_token_budget = %v, want 0", budget)
			}
		})
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document with `max_tokens:255`, one below the 256 force-zero threshold.
//  2. Assert the budget is forced to 0.
func TestThinkingTokenBudgetResolveJustBelowThresholdForcesZero(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":255}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(0) {
		t.Errorf("thinking_token_budget = %v, want 0", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document with `max_tokens:256`, exactly at the force-zero threshold.
//  2. Assert the half-split default applies instead of forcing zero.
func TestThinkingTokenBudgetResolveBoundaryAtThresholdKeepsHalfSplit(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":256}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(128) {
		t.Errorf("thinking_token_budget = %v, want 128", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document whose requested budget far exceeds max_tokens.
//  2. Assert the budget is clamped to max_tokens minus the content headroom.
func TestThinkingTokenBudgetResolveContentHeadroomClamp(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":4096,"thinking_token_budget":10000}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(4032) {
		t.Errorf("thinking_token_budget = %v, want 4032 (max_tokens - 64 headroom)", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document with a very large `max_tokens` and requested budget.
//  2. Assert the budget is clamped to the absolute maximum, since max_tokens minus the content headroom would otherwise exceed it.
func TestThinkingTokenBudgetResolveAbsoluteMaxClamp(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":200000,"thinking_token_budget":150000}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(96_000) {
		t.Errorf("thinking_token_budget = %v, want 96000", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document whose requested budget already fits under the content headroom.
//  2. Assert the client's value is preserved unclamped.
func TestThinkingTokenBudgetResolvePreservesClientValueUnderHeadroom(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":4096,"thinking_token_budget":500}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != uint64(500) {
		t.Errorf("thinking_token_budget = %v, want 500 (unclamped)", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document whose `thinking_token_budget` is a numeric string.
//  2. Assert the value is left completely untouched, since it fails the uint64 coercion and bypasses every clamp below.
func TestThinkingTokenBudgetResolveStringValueBypassesClamp(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":1000,"thinking_token_budget":"99999999"}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if got, _ := document.Get("thinking_token_budget"); got != "99999999" {
		t.Errorf("thinking_token_budget = %#v, want the original string untouched", got)
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document with no `max_tokens`.
//  2. Assert `thinking_token_budget` is not created.
func TestThinkingTokenBudgetResolveSkipsWhenMaxTokensAbsent(t *testing.T) {
	document := parseTestDocument(t, `{}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if document.Has("thinking_token_budget") {
		t.Error("thinking_token_budget must not be created when max_tokens is absent")
	}
}

// Test flow:
//  1. Apply `thinkingTokenBudgetResolve()` under the kimi profile to a document with `max_tokens:0`.
//  2. Assert `thinking_token_budget` is not created.
func TestThinkingTokenBudgetResolveSkipsWhenMaxTokensZero(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":0}`)
	if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Profile: kimiProfile}); err != nil {
		t.Fatalf("thinkingTokenBudgetResolve() = %v, want nil", err)
	}
	if document.Has("thinking_token_budget") {
		t.Error("thinking_token_budget must not be created when max_tokens is zero")
	}
}

// Test flow:
//  1. Apply `safetyIdentifier()` to a document with a `safety_identifier` under the kimi profile.
//  2. Assert the value is kept unchanged.
func TestSafetyIdentifierKeepsAndValidatesForHookProfile(t *testing.T) {
	document := parseTestDocument(t, `{"safety_identifier":"hashed-user-abc"}`)
	if err := safetyIdentifier()(RuleContext{Document: document, Param: "safety_identifier", Profile: kimiProfile}); err != nil {
		t.Fatalf("safetyIdentifier() = %v, want nil", err)
	}
	if got, _ := document.Get("safety_identifier"); got != "hashed-user-abc" {
		t.Errorf("safety_identifier = %v, want unchanged", got)
	}
}

// Test flow:
//  1. Apply `safetyIdentifier()` under the kimi profile to a `safety_identifier` one byte longer than `safetyIdentifierMaxLen`.
//  2. Assert it returns an error.
func TestSafetyIdentifierRejectsOverLengthForHookProfile(t *testing.T) {
	long := make([]byte, safetyIdentifierMaxLen+1)
	for i := range long {
		long[i] = 'x'
	}
	document := parseTestDocument(t, `{"safety_identifier":"`+string(long)+`"}`)
	err := safetyIdentifier()(RuleContext{Document: document, Param: "safety_identifier", Profile: kimiProfile})
	if err == nil {
		t.Fatal("safetyIdentifier() = nil, want an error")
	}
}

// Test flow:
//  1. For both the default (nil) and minimax profiles, apply `safetyIdentifier()` to a document with a `safety_identifier`.
//  2. Assert the field is stripped for every profile without the hook.
func TestSafetyIdentifierStripsWithoutHook(t *testing.T) {
	for _, profile := range []*Profile{nil, minimaxProfile} {
		document := parseTestDocument(t, `{"safety_identifier":"anything"}`)
		if err := safetyIdentifier()(RuleContext{Document: document, Param: "safety_identifier", Profile: profile}); err != nil {
			t.Fatalf("safetyIdentifier() = %v, want nil", err)
		}
		if document.Has("safety_identifier") {
			t.Errorf("safety_identifier must be stripped for profile %v", profile)
		}
	}
}

// Test flow:
//  1. Apply `reasoningSplit()` to `{"reasoning_split":false}` under the minimax profile.
//  2. Assert the value passes through unchanged.
func TestReasoningSplitPassesThroughForHookProfile(t *testing.T) {
	document := parseTestDocument(t, `{"reasoning_split":false}`)
	if err := reasoningSplit()(RuleContext{Document: document, Param: "reasoning_split", Profile: minimaxProfile}); err != nil {
		t.Fatalf("reasoningSplit() = %v, want nil", err)
	}
	if got, _ := document.Get("reasoning_split"); got != false {
		t.Errorf("reasoning_split = %v, want unchanged (false)", got)
	}
}

// Test flow:
//  1. Apply `reasoningSplit()` under the minimax profile to a document with no `reasoning_split` field.
//  2. Assert the field stays absent, since the caller decides whether reasoning arrives split.
func TestReasoningSplitLeavesAnAbsentFieldAbsent(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":4096}`)
	if err := reasoningSplit()(RuleContext{Document: document, Param: "reasoning_split", Profile: minimaxProfile}); err != nil {
		t.Fatalf("reasoningSplit() = %v, want nil", err)
	}
	if document.Has("reasoning_split") {
		t.Error("reasoning_split was invented; the caller decides whether reasoning arrives split")
	}
}

// Test flow:
//  1. For both the default (nil) and kimi profiles, apply `reasoningSplit()` to a document with `reasoning_split:true`.
//  2. Assert the field is stripped for every profile without the hook.
func TestReasoningSplitStripsWithoutHook(t *testing.T) {
	for _, profile := range []*Profile{nil, kimiProfile} {
		document := parseTestDocument(t, `{"reasoning_split":true}`)
		if err := reasoningSplit()(RuleContext{Document: document, Param: "reasoning_split", Profile: profile}); err != nil {
			t.Fatalf("reasoningSplit() = %v, want nil", err)
		}
		if document.Has("reasoning_split") {
			t.Errorf("reasoning_split must be stripped for profile %v", profile)
		}
	}
}

// Test flow:
//  1. Apply `forceZeroPenalty()` to a document with `frequency_penalty:0.5` under the kimi profile.
//  2. Assert the value is overwritten to 0.
func TestForceZeroPenaltyOverwritesPresentFieldForHookProfile(t *testing.T) {
	document := parseTestDocument(t, `{"frequency_penalty":0.5}`)
	if err := forceZeroPenalty()(RuleContext{Document: document, Param: "frequency_penalty", Profile: kimiProfile}); err != nil {
		t.Fatalf("forceZeroPenalty() = %v, want nil", err)
	}
	if got, _ := document.Get("frequency_penalty"); got != 0.0 {
		t.Errorf("frequency_penalty = %v, want 0", got)
	}
}

// Test flow:
//  1. Apply `forceZeroPenalty()` under the kimi profile to a document with no `frequency_penalty`.
//  2. Assert the field stays absent, since the rule only overwrites, never creates.
func TestForceZeroPenaltyLeavesAbsentFieldAbsent(t *testing.T) {
	document := parseTestDocument(t, `{}`)
	if err := forceZeroPenalty()(RuleContext{Document: document, Param: "frequency_penalty", Profile: kimiProfile}); err != nil {
		t.Fatalf("forceZeroPenalty() = %v, want nil", err)
	}
	if document.Has("frequency_penalty") {
		t.Error("forceZeroPenalty must not create the field when absent (overwrite-only)")
	}
}

// Test flow:
//  1. For both the default (nil) and minimax profiles, apply `forceZeroPenalty()` to a document with `presence_penalty:-0.5`.
//  2. Assert the value is left unchanged for every profile without the hook.
func TestForceZeroPenaltyNoOpWithoutHook(t *testing.T) {
	for _, profile := range []*Profile{nil, minimaxProfile} {
		document := parseTestDocument(t, `{"presence_penalty":-0.5}`)
		if err := forceZeroPenalty()(RuleContext{Document: document, Param: "presence_penalty", Profile: profile}); err != nil {
			t.Fatalf("forceZeroPenalty() = %v, want nil", err)
		}
		if got, _ := document.Get("presence_penalty"); got != json.Number("-0.5") {
			t.Errorf("presence_penalty = %v, want unchanged for profile %v", got, profile)
		}
	}
}

func boolLiteral(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// Test flow:
//  1. Run table cases of `thinkingTokenBudgetResolve()` under the kimi profile, varying `max_tokens` below and at the cutoff, with and without a client-set `chat_template_kwargs.thinking`.
//  2. Assert a below-cutoff budget forces `chat_template_kwargs.thinking` to false, overruling a client that asked to think.
//  3. Assert an at-cutoff budget leaves the template's thinking flag untouched.
func TestASmallBudgetSilencesKimiInTheTemplateToo(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		body         string
		wantThinking any
	}{
		{
			name:         "below the cutoff the template is told not to think",
			body:         `{"max_tokens":144}`,
			wantThinking: false,
		},
		{
			name: "at the cutoff the template is left alone",
			body: `{"max_tokens":256}`,
		},
		{
			name:         "a client that asked to think is overruled",
			body:         `{"max_tokens":144,"chat_template_kwargs":{"thinking":true}}`,
			wantThinking: false,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			document := parseTestDocument(t, testCase.body)

			if err := thinkingTokenBudgetResolve()(RuleContext{Document: document, Param: "thinking_token_budget", Profile: kimiProfile}); err != nil {
				t.Fatalf("resolve = %v, want nil", err)
			}

			kwargs, _, _ := document.ObjectField("chat_template_kwargs")
			if testCase.wantThinking == nil {
				if _, held := kwargs["thinking"]; held {
					t.Fatalf("chat_template_kwargs = %v, want no thinking flag", kwargs)
				}
				return
			}
			if kwargs["thinking"] != testCase.wantThinking {
				t.Fatalf("chat_template_kwargs.thinking = %v, want %v", kwargs["thinking"], testCase.wantThinking)
			}
		})
	}
}

// Test flow:
//  1. Normalize a request for every combination of routed model (unrouted, kimi, minimax, deepseek) and every known reasoning-effort level.
//  2. Assert the normalized body's `reasoning_effort` always matches the requested level, since a caller asking for one is not second-guessed on routes that ignore it — only DeepSeek's renderer actually reads the field.
func TestReasoningEffortReachesEveryRoute(t *testing.T) {
	for _, model := range []string{"", kimiModelID, minimaxModelID, deepseekModelID} {
		for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			body := `{"messages":[{"role":"user","content":"x"}],"max_tokens":4096,"reasoning_effort":"` + effort + `"}`
			document := normalizedDocument(t, body, model)
			if document["reasoning_effort"] != effort {
				t.Errorf("model %q: reasoning_effort = %v, want %q", model, document["reasoning_effort"], effort)
			}
		}
	}
}

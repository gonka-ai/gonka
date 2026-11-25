package filters

import (
	"fmt"
	"net/http"
	"testing"
)

// Test flow:
//  1. Run table cases of `capOutputTokens(value, bypassLimit, limits)`, varying a zero value with and without admin bypass, a value under the cap, and a value over the cap with and without bypass.
//  2. Assert each case's result, pinning that a zero value always means "unset, use default" regardless of bypass.
func TestTokensCapOutputTokens(t *testing.T) {
	limits := outputTokenLimits{DefaultMaxTokens: 100, MaxTokensCap: 200}
	tests := []struct {
		name        string
		value       uint64
		bypassLimit bool
		want        uint64
	}{
		{"zero value uses default", 0, false, 100},
		{"zero value uses default even under admin bypass", 0, true, 100},
		{"value under cap kept as-is", 150, false, 150},
		{"value over cap clamped", 300, false, 200},
		{"value over cap admin bypass keeps raw value", 300, true, 300},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := capOutputTokens(testCase.value, testCase.bypassLimit, limits)
			if got != testCase.want {
				t.Errorf("capOutputTokens(%d, %v) = %d, want %d", testCase.value, testCase.bypassLimit, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `capOutputTokens` with a default (10000) larger than the cap (4096), varying an unset request, an admin's unset request, an explicit ask matching the default, and an admin's explicit ask.
//  2. Assert an unset request takes the operator's default in full, uncapped, while an explicit ask is clamped to the cap unless bypassed — the cap bounds what a client may ask for, not what an operator grants a client that asks for nothing.
func TestTokensTheCapBoundsTheAskAndNotTheDefault(t *testing.T) {
	limits := outputTokenLimits{DefaultMaxTokens: 10_000, MaxTokensCap: 4_096}
	tests := []struct {
		name        string
		value       uint64
		bypassLimit bool
		want        uint64
	}{
		{"an unset request takes the operator's default in full", 0, false, 10_000},
		{"an admin's unset request takes it too", 0, true, 10_000},
		{"an explicit ask for the same number is still clamped", 10_000, false, 4_096},
		{"an admin's explicit ask bypasses the cap", 10_000, true, 10_000},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := capOutputTokens(testCase.value, testCase.bypassLimit, limits)
			if got != testCase.want {
				t.Errorf("capOutputTokens(%d, %v) = %d, want %d", testCase.value, testCase.bypassLimit, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Call `normalizedOutputTokenLimits` with a zero-valued `outputTokenLimits`.
//  2. Assert it falls back to `DefaultRequestMaxTokens` and `RequestMaxTokensCap`.
func TestTokensNormalizedLimitsFallBackWhenZero(t *testing.T) {
	got := normalizedOutputTokenLimits(outputTokenLimits{})
	want := outputTokenLimits{DefaultMaxTokens: DefaultRequestMaxTokens, MaxTokensCap: RequestMaxTokensCap}
	if got != want {
		t.Errorf("normalizedOutputTokenLimits({}) = %+v, want %+v", got, want)
	}
}

// Test flow:
//  1. Call `normalizedOutputTokenLimits` with explicit default and cap values.
//  2. Assert both values are kept unchanged.
func TestTokensNormalizedLimitsKeepExplicitValues(t *testing.T) {
	got := normalizedOutputTokenLimits(outputTokenLimits{DefaultMaxTokens: 10, MaxTokensCap: 20})
	want := outputTokenLimits{DefaultMaxTokens: 10, MaxTokensCap: 20}
	if got != want {
		t.Errorf("normalizedOutputTokenLimits() = %+v, want %+v", got, want)
	}
}

// applyLimits runs the full decode+cap round trip so branch tests can assert on document and view.
func applyLimits(t *testing.T, body string, options Options) (*Document, requestView) {
	t.Helper()
	document, err := ParseDocument([]byte(body))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	view, err := decodeRequestView(document)
	if err != nil {
		t.Fatalf("decodeRequestView: %v", err)
	}
	applyOutputTokenLimits(document, &view, options, view.Model, BuiltInProfileFor(view.Model))
	return document, view
}

// Test flow:
//  1. Apply output-token limits via the `applyLimits` helper to an empty body.
//  2. Assert the view's MaxTokens takes the default and MaxCompletionTokens stays zero.
//  3. Assert the document's max_tokens is set to the default and max_completion_tokens is not created.
func TestTokensApplyLimitsNeitherSetUsesDefault(t *testing.T) {
	document, view := applyLimits(t, `{}`, Options{DefaultMaxTokens: 1000, MaxTokensCap: 2000})

	if view.MaxTokens != 1000 {
		t.Errorf("view.MaxTokens = %d, want 1000", view.MaxTokens)
	}
	if view.MaxCompletionTokens != 0 {
		t.Errorf("view.MaxCompletionTokens = %d, want 0", view.MaxCompletionTokens)
	}
	if got, _ := document.Get("max_tokens"); got != uint64(1000) {
		t.Errorf("document max_tokens = %v, want 1000", got)
	}
	if document.Has("max_completion_tokens") {
		t.Error("document must not gain max_completion_tokens when neither field was set")
	}
}

// Test flow:
//  1. Run table cases of `applyLimits` with only max_tokens set, varying a value under the cap, over the cap, over the cap with admin bypass, and an explicit zero.
//  2. Assert the view's MaxTokens and the document's max_tokens match the expected result in every case.
//  3. Assert MaxCompletionTokens stays zero and max_completion_tokens is never created.
func TestTokensApplyLimitsOnlyMaxTokensSet(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens uint64
		admin     bool
		want      uint64
	}{
		{"under cap kept as-is", 1500, false, 1500},
		{"over cap clamped", 3000, false, 2000},
		{"over cap admin bypasses", 3000, true, 3000},
		{"explicit zero treated as unset, uses default", 0, false, 1000},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"max_tokens":%d}`, testCase.maxTokens)
			document, view := applyLimits(t, body, Options{Admin: testCase.admin, DefaultMaxTokens: 1000, MaxTokensCap: 2000})

			if view.MaxTokens != testCase.want {
				t.Errorf("view.MaxTokens = %d, want %d", view.MaxTokens, testCase.want)
			}
			if view.MaxCompletionTokens != 0 {
				t.Errorf("view.MaxCompletionTokens = %d, want 0", view.MaxCompletionTokens)
			}
			if got, _ := document.Get("max_tokens"); got != testCase.want {
				t.Errorf("document max_tokens = %v, want %d", got, testCase.want)
			}
			if document.Has("max_completion_tokens") {
				t.Error("document must not gain max_completion_tokens in the max_tokens-only branch")
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `applyLimits` with only max_completion_tokens set, varying a value under the cap, over the cap, over the cap with admin bypass, and an explicit zero.
//  2. Assert both view fields and both document fields mirror the same resolved value, since a completion-tokens-only request mirrors into both fields.
func TestTokensApplyLimitsOnlyMaxCompletionTokensSet(t *testing.T) {
	tests := []struct {
		name  string
		value uint64
		admin bool
		want  uint64
	}{
		{"under cap kept as-is", 1500, false, 1500},
		{"over cap clamped", 3000, false, 2000},
		{"over cap admin bypasses", 3000, true, 3000},
		{"explicit zero treated as unset, uses default", 0, false, 1000},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"max_completion_tokens":%d}`, testCase.value)
			document, view := applyLimits(t, body, Options{Admin: testCase.admin, DefaultMaxTokens: 1000, MaxTokensCap: 2000})

			if view.MaxTokens != testCase.want {
				t.Errorf("view.MaxTokens = %d, want %d", view.MaxTokens, testCase.want)
			}
			if view.MaxCompletionTokens != testCase.want {
				t.Errorf("view.MaxCompletionTokens = %d, want %d", view.MaxCompletionTokens, testCase.want)
			}
			if got, _ := document.Get("max_tokens"); got != testCase.want {
				t.Errorf("document max_tokens (mirrored) = %v, want %d", got, testCase.want)
			}
			if got, _ := document.Get("max_completion_tokens"); got != testCase.want {
				t.Errorf("document max_completion_tokens = %v, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `applyLimits` with both max_tokens and max_completion_tokens set, varying which field is smaller, both over the cap, an admin bypass, and an explicit zero on one field.
//  2. Assert the view and document both settle on the smaller of the two values (or the admin-bypassed raw minimum), with an explicit zero treated as unset before the comparison.
func TestTokensApplyLimitsBothSet(t *testing.T) {
	tests := []struct {
		name                string
		maxTokens           uint64
		maxCompletionTokens uint64
		admin               bool
		want                uint64
	}{
		{"both under cap, completion smaller wins", 1800, 1200, false, 1200},
		{"both under cap, max_tokens smaller wins", 1200, 1800, false, 1200},
		{"both over cap collapse to the shared cap", 5000, 8000, false, 2000},
		{"admin bypass keeps the raw min uncapped", 5000, 8000, true, 5000},
		{"max_tokens explicit zero defaults before the min compare", 0, 5000, false, 1000},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"max_tokens":%d,"max_completion_tokens":%d}`, testCase.maxTokens, testCase.maxCompletionTokens)
			document, view := applyLimits(t, body, Options{Admin: testCase.admin, DefaultMaxTokens: 1000, MaxTokensCap: 2000})

			if view.MaxTokens != testCase.want {
				t.Errorf("view.MaxTokens = %d, want %d", view.MaxTokens, testCase.want)
			}
			if view.MaxCompletionTokens != testCase.want {
				t.Errorf("view.MaxCompletionTokens = %d, want %d", view.MaxCompletionTokens, testCase.want)
			}
			if got, _ := document.Get("max_tokens"); got != testCase.want {
				t.Errorf("document max_tokens = %v, want %d", got, testCase.want)
			}
			if got, _ := document.Get("max_completion_tokens"); got != testCase.want {
				t.Errorf("document max_completion_tokens = %v, want %d", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Parse a document with model, stream, max_tokens, max_completion_tokens, and n all set.
//  2. Decode it into a requestView.
//  3. Assert every field matches the expected view.
func TestTokensDecodeRequestViewHappyPath(t *testing.T) {
	document, err := ParseDocument([]byte(`{"model":"qwen","stream":true,"max_tokens":10,"max_completion_tokens":20,"n":2}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	view, err := decodeRequestView(document)
	if err != nil {
		t.Fatalf("decodeRequestView: %v", err)
	}
	want := requestView{Model: "qwen", Stream: true, MaxTokens: 10, MaxCompletionTokens: 20, N: 2}
	if view != want {
		t.Errorf("decodeRequestView() = %+v, want %+v", view, want)
	}
}

// Test flow:
//  1. Parse an empty document.
//  2. Decode it into a requestView.
//  3. Assert the view is the zero value.
func TestTokensDecodeRequestViewAbsentFieldsAreZeroValue(t *testing.T) {
	document, err := ParseDocument([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	view, err := decodeRequestView(document)
	if err != nil {
		t.Fatalf("decodeRequestView: %v", err)
	}
	if view != (requestView{}) {
		t.Errorf("decodeRequestView({}) = %+v, want zero value", view)
	}
}

// Test flow:
//  1. Run table cases of `decodeRequestView` against each of the five typed fields (model, stream, max_tokens, max_completion_tokens, n) given a value of the wrong type.
//  2. Assert each case's exact error message and that ErrorStatus is 400.
func TestTokensDecodeRequestViewTypeErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"model must be a string", `{"model":42}`, "parse request: model must be a string"},
		{"stream must be a boolean", `{"stream":"yes"}`, "parse request: stream must be a boolean"},
		{"max_tokens must be a non-negative integer", `{"max_tokens":"abc"}`, "parse request: max_tokens must be a non-negative integer"},
		{"max_completion_tokens must be a non-negative integer", `{"max_completion_tokens":-1}`, "parse request: max_completion_tokens must be a non-negative integer"},
		{"n must be a non-negative integer", `{"n":-1}`, "parse request: n must be a non-negative integer"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			document, err := ParseDocument([]byte(testCase.body))
			if err != nil {
				t.Fatalf("ParseDocument: %v", err)
			}
			_, err = decodeRequestView(document)
			if err == nil {
				t.Fatal("decodeRequestView(): want error, got nil")
			}
			if err.Error() != testCase.wantErr {
				t.Errorf("decodeRequestView() error = %q, want %q", err.Error(), testCase.wantErr)
			}
			if got := ErrorStatus(err, 0); got != http.StatusBadRequest {
				t.Errorf("ErrorStatus() = %d, want %d", got, http.StatusBadRequest)
			}
		})
	}
}

// Test flow:
//  1. Apply `raiseOutputTokensToProfileFloor()` under the kimi profile to max_tokens and max_completion_tokens set to 0, 1 and 15.
//  2. Assert each is raised to the profile's floor: below it Kimi-K2.6 emits only the closing think tag.
func TestAKimiBudgetBelowItsFloorIsRaisedToIt(t *testing.T) {
	for _, param := range []string{"max_tokens", "max_completion_tokens"} {
		for _, value := range []string{"0", "1", "15"} {
			t.Run(param+"="+value, func(t *testing.T) {
				document := parseTestDocument(t, `{"`+param+`":`+value+`}`)
				if err := raiseOutputTokensToProfileFloor()(RuleContext{Document: document, Param: param, Profile: kimiProfile}); err != nil {
					t.Fatalf("raiseOutputTokensToProfileFloor() = %v, want nil", err)
				}
				if got, _ := document.Uint(param); got != kimiOutputTokensFloor {
					t.Errorf("%s = %d, want the kimi floor %d", param, got, kimiOutputTokensFloor)
				}
			})
		}
	}
}

// Test flow:
//  1. Apply `raiseOutputTokensToProfileFloor()` under the kimi profile to a max_tokens at and above its floor.
//  2. Assert the value is left as the client sent it.
func TestAKimiBudgetAtOrAboveItsFloorIsKept(t *testing.T) {
	for _, value := range []uint64{kimiOutputTokensFloor, 300} {
		document := parseTestDocument(t, fmt.Sprintf(`{"max_tokens":%d}`, value))
		if err := raiseOutputTokensToProfileFloor()(RuleContext{Document: document, Param: "max_tokens", Profile: kimiProfile}); err != nil {
			t.Fatalf("raiseOutputTokensToProfileFloor() = %v, want nil", err)
		}
		if got, _ := document.Uint("max_tokens"); got != value {
			t.Errorf("max_tokens = %d, want %d kept", got, value)
		}
	}
}

// Test flow:
//  1. Apply `raiseOutputTokensToProfileFloor()` to a document with max_tokens:0 under the default (nil), minimax, and an empty profile.
//  2. Assert the value is left at 0 for every profile without a floor, leaving it for the downstream refusal.
func TestOnlyAProfileWithAFloorRaisesTheBudget(t *testing.T) {
	for _, profile := range []*Profile{nil, minimaxProfile, &Profile{}} {
		document := parseTestDocument(t, `{"max_tokens":0}`)
		if err := raiseOutputTokensToProfileFloor()(RuleContext{Document: document, Param: "max_tokens", Profile: profile}); err != nil {
			t.Fatalf("raiseOutputTokensToProfileFloor() = %v, want nil", err)
		}
		if got, _ := document.Uint("max_tokens"); got != 0 {
			t.Errorf("max_tokens = %d for profile %v, want the zero left for the refusal", got, profile)
		}
	}
}

// Test flow:
//  1. Apply `rejectNonPositiveOutputTokens()` to a document with max_tokens:0 under the default (nil), kimi, minimax, and an empty profile.
//  2. Assert every profile is rejected, since the refusal stays absolute — a profile with a floor gets its zero raised by `raiseOutputTokensToProfileFloor` running ahead of it, because capOutputTokens would otherwise read the zero as "unset" and hand it the default rather than the profile's floor.
func TestRejectNonPositiveOutputTokensRejectsZeroOnEveryProfile(t *testing.T) {
	for _, profile := range []*Profile{nil, kimiProfile, minimaxProfile, &Profile{}} {
		document := parseTestDocument(t, `{"max_tokens":0}`)
		if err := rejectNonPositiveOutputTokens()(RuleContext{Document: document, Param: "max_tokens", Profile: profile}); err == nil {
			t.Errorf("rejectNonPositiveOutputTokens() = nil for profile %v, want a rejection", profile)
		}
	}
}

// Test flow:
//  1. Apply `rejectNonPositiveOutputTokens()` to a document with max_tokens set to 0, -1, and -100.
//  2. Assert every value is rejected with the must-be-greater-than-0 error.
func TestRejectNonPositiveOutputTokensRejectsZeroAndNegative(t *testing.T) {
	for _, value := range []string{"0", "-1", "-100"} {
		t.Run(value, func(t *testing.T) {
			document := parseTestDocument(t, `{"max_tokens":`+value+`}`)
			err := rejectNonPositiveOutputTokens()(RuleContext{Document: document, Param: "max_tokens"})
			want := "max_tokens: must be greater than 0"
			if err == nil || err.Error() != want {
				t.Errorf("rejectNonPositiveOutputTokens() = %v, want %q", err, want)
			}
		})
	}
}

// Test flow:
//  1. Apply `rejectNonPositiveOutputTokens()` to a document with max_completion_tokens:5.
//  2. Assert it returns no error.
func TestRejectNonPositiveOutputTokensAcceptsPositive(t *testing.T) {
	document := parseTestDocument(t, `{"max_completion_tokens":5}`)
	err := rejectNonPositiveOutputTokens()(RuleContext{Document: document, Param: "max_completion_tokens"})
	if err != nil {
		t.Fatalf("rejectNonPositiveOutputTokens() = %v, want nil", err)
	}
}

// Test flow:
//  1. Apply `rejectNonPositiveOutputTokens()` to a document with no max_tokens.
//  2. Assert it returns no error.
func TestRejectNonPositiveOutputTokensAbsentIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{}`)
	if err := rejectNonPositiveOutputTokens()(RuleContext{Document: document, Param: "max_tokens"}); err != nil {
		t.Fatalf("rejectNonPositiveOutputTokens() = %v, want nil", err)
	}
}

// Test flow:
//  1. Apply `rejectNonPositiveOutputTokens()` to a document with a non-numeric max_tokens.
//  2. Assert it returns no error, since type-checking happens elsewhere.
func TestRejectNonPositiveOutputTokensNonNumericIsNoOp(t *testing.T) {
	document := parseTestDocument(t, `{"max_tokens":"abc"}`)
	if err := rejectNonPositiveOutputTokens()(RuleContext{Document: document, Param: "max_tokens"}); err != nil {
		t.Fatalf("rejectNonPositiveOutputTokens() = %v, want nil (type-checked elsewhere)", err)
	}
}

// Test flow:
//  1. Apply output-token limits for a model with a per-model override and for an unlisted model, using the same global default and cap.
//  2. Assert the overridden model's MaxTokens uses the per-model default while the unlisted model falls back to the global default.
func TestTokensPerModelOverrideBeatsTheGlobalDefaultAndCap(t *testing.T) {
	options := Options{
		DefaultMaxTokens: 1000,
		MaxTokensCap:     2000,
		ModelTokenLimits: func(model string) (uint64, uint64) {
			if model == "qwen" {
				return 5_000, 8_000
			}
			return 0, 0
		},
	}

	_, overridden := applyLimits(t, `{"model":"qwen"}`, options)
	_, global := applyLimits(t, `{"model":"other"}`, options)

	if overridden.MaxTokens != 5_000 {
		t.Errorf("qwen default = %d, want the per-model 5000", overridden.MaxTokens)
	}
	if global.MaxTokens != 1000 {
		t.Errorf("unlisted model default = %d, want the global 1000", global.MaxTokens)
	}
}

// Test flow:
//  1. Apply output-token limits for a model with a per-model cap lower than both the request and the global cap.
//  2. Assert MaxTokens is clamped to the per-model cap.
func TestTokensPerModelOverrideCapClampsTheRequestedValue(t *testing.T) {
	options := Options{
		DefaultMaxTokens: 1000,
		MaxTokensCap:     2000,
		ModelTokenLimits: func(string) (uint64, uint64) { return 0, 1500 },
	}

	_, view := applyLimits(t, `{"model":"qwen","max_tokens":1800}`, options)

	if view.MaxTokens != 1500 {
		t.Errorf("max_tokens = %d, want the per-model cap 1500", view.MaxTokens)
	}
}

// Test flow:
//  1. Apply output-token limits for a model whose per-model cap is 50.
//  2. Assert MaxTokens is clamped to that cap: no shared floor outranks the operator's cap any more.
func TestTokensASmallPerModelCapHolds(t *testing.T) {
	options := Options{
		DefaultMaxTokens: 1000,
		MaxTokensCap:     2000,
		ModelTokenLimits: func(string) (uint64, uint64) { return 0, 50 },
	}

	_, view := applyLimits(t, `{"model":"qwen","max_tokens":1800}`, options)

	if view.MaxTokens != 50 {
		t.Errorf("max_tokens = %d, want the per-model cap 50", view.MaxTokens)
	}
}

// Test flow:
//  1. Normalize a request with max_tokens 8 once per routed model: kimi, minimax, and a plain "Qwen/Test" model.
//  2. Assert the declared MaxTokens is 8 on minimax and the plain model, and Kimi's own floor on Kimi.
func TestTokensOnlyKimiRaisesASmallBudget(t *testing.T) {
	for model, want := range map[string]uint64{kimiModelID: kimiOutputTokensFloor, minimaxModelID: 8, "Qwen/Test": 8} {
		t.Run(model, func(t *testing.T) {
			result, err := NormalizeRequest([]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":8}`),
				Options{RoutedModel: model, DefaultMaxTokens: 3072, MaxTokensCap: 4096})
			if err != nil {
				t.Fatalf("NormalizeRequest() = %v, want nil", err)
			}
			if result.MaxTokens != want {
				t.Errorf("MaxTokens = %d, want %d", result.MaxTokens, want)
			}
		})
	}
}

// Test flow:
//  1. Configure a default of 8 output tokens and send Kimi, whose preset floors output at 16, a request with no budget.
//  2. Assert the default is raised to the floor, the same as a budget the client named.
func TestDefaultOutputBudgetIsRaisedToTheProfileFloor(t *testing.T) {
	body := []byte(`{"model":"` + kimiModelID + `","messages":[{"role":"user","content":"x"}]}`)

	result, err := NormalizeRequest(body, Options{DefaultMaxTokens: 8, MaxTokensCap: RequestMaxTokensCap})

	if err != nil {
		t.Fatalf("NormalizeRequest() error = %v", err)
	}
	if result.MaxTokens != kimiOutputTokensFloor {
		t.Errorf("max_tokens = %d, want the floor %d", result.MaxTokens, kimiOutputTokensFloor)
	}
}

// Test flow:
//  1. Give a model an output floor of 3000 with a default of 500 and a cap of 1000, and send a request with no budget, once as a client and once as an admin.
//  2. Assert the client gets the cap, not the floor: the cap prices the reservation, whatever raised the default. The admin, who skips the cap, gets the floor.
func TestFloorNeverRaisesTheDefaultAboveTheCap(t *testing.T) {
	floor := uint64(3000)
	profile := composeForTest(t, presetsTestQwenModel, ModelSetting{Filters: FilterKnobs{OutputTokensFloor: &floor}})
	body := []byte(`{"model":"` + presetsTestQwenModel + `","messages":[{"role":"user","content":"x"}]}`)
	options := Options{DefaultMaxTokens: 500, MaxTokensCap: 1000, Profiles: NewProfileSet(map[string]*Profile{presetsTestQwenModel: profile})}

	client, clientErr := NormalizeRequest(body, options)
	options.Admin = true
	admin, adminErr := NormalizeRequest(body, options)

	if clientErr != nil || adminErr != nil {
		t.Fatalf("errors = %v, %v", clientErr, adminErr)
	}
	if client.MaxTokens != 1000 {
		t.Errorf("client max_tokens = %d, want the cap 1000", client.MaxTokens)
	}
	if admin.MaxTokens != 3000 {
		t.Errorf("admin max_tokens = %d, want the floor 3000", admin.MaxTokens)
	}
}

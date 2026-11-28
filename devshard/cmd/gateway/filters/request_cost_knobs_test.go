package filters

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

const requestCostKnobModel = "acme/Cost-1"

func requestCostOptions(t *testing.T, knobs FilterKnobs) Options {
	t.Helper()
	profile := composeForTest(t, requestCostKnobModel, ModelSetting{Filters: knobs})
	return Options{
		RoutedModel:      requestCostKnobModel,
		Profiles:         NewProfileSet(map[string]*Profile{requestCostKnobModel: profile}),
		DefaultMaxTokens: 3072,
		MaxTokensCap:     4096,
	}
}

func normalizeWithCostKnobs(t *testing.T, knobs FilterKnobs, body string) error {
	t.Helper()
	_, err := NormalizeRequest([]byte(body), requestCostOptions(t, knobs))
	return err
}

func pointerTo[Value any](value Value) *Value {
	return &value
}

func smallTools(count int) string {
	return toolsBudgetBody(repeatedTools(count, `{"type":"function","function":{"name":"x"}}`))
}

// Test flow:
//  1. Read the effective knobs of the default profile, of every preset in the catalog, and of a composed setting that names no request-cost knob.
//  2. Assert each carries tools_max_count 128 and pattern_budget_bytes 4096, the values the rules enforced before the knobs existed.
func TestRequestCostKnobsDefaultToTheFormerLimits(t *testing.T) {
	effective := map[string]FilterKnobs{
		"default_profile": (*Profile)(nil).Knobs(),
		"composed":        composeForTest(t, requestCostKnobModel, ModelSetting{Filters: FilterKnobs{Thinking: pointerTo(ThinkingStrip)}}).Knobs(),
	}
	for name, knobs := range PresetCatalog() {
		effective["preset_"+string(name)] = knobs
	}
	for name, knobs := range effective {
		t.Run(name, func(t *testing.T) {
			if knobs.ToolsMaxCount == nil || *knobs.ToolsMaxCount != 128 {
				t.Errorf("tools_max_count = %v, want 128", knobs.ToolsMaxCount)
			}
			if knobs.PatternBudgetBytes == nil || *knobs.PatternBudgetBytes != 4096 {
				t.Errorf("pattern_budget_bytes = %v, want 4096", knobs.PatternBudgetBytes)
			}
		})
	}
}

// Test flow:
//  1. Store tools_max_count 3 for a model and send it 3 tools, then 4 tools.
//  2. Assert 3 are accepted and 4 are rejected with the stored limit named.
func TestToolsMaxCountKnobLowersTheLimit(t *testing.T) {
	knobs := FilterKnobs{ToolsMaxCount: pointerTo(3)}
	if err := normalizeWithCostKnobs(t, knobs, smallTools(3)); err != nil {
		t.Fatalf("3 tools under a limit of 3 = %v, want acceptance", err)
	}
	requireToolsRejection(t, normalizeWithCostKnobs(t, knobs, smallTools(4)), "tools: array length 4 exceeds limit 3")
}

// Test flow:
//  1. Send one tool more than the default limit to the default profile, then to a model storing tools_max_count at the ceiling.
//  2. Assert the default profile rejects it and the raised model accepts it.
//  3. Assert the raised model accepts exactly the ceiling and rejects one tool more, naming the ceiling.
func TestToolsMaxCountKnobRaisesTheLimitUpToTheCeiling(t *testing.T) {
	requireToolsRejection(t, normalizeWithCostKnobs(t, FilterKnobs{}, smallTools(defaultToolsMaxCount+1)), "tools: array length 129 exceeds limit 128")
	knobs := FilterKnobs{ToolsMaxCount: pointerTo(MaxToolsMaxCount)}
	if err := normalizeWithCostKnobs(t, knobs, smallTools(defaultToolsMaxCount+1)); err != nil {
		t.Fatalf("129 tools under a raised limit = %v, want acceptance", err)
	}
	if err := normalizeWithCostKnobs(t, knobs, smallTools(MaxToolsMaxCount)); err != nil {
		t.Fatalf("%d tools at the ceiling = %v, want acceptance", MaxToolsMaxCount, err)
	}
	requireToolsRejection(t, normalizeWithCostKnobs(t, knobs, smallTools(MaxToolsMaxCount+1)), "tools: array length 257 exceeds limit 256")
}

// Test flow:
//  1. Store pattern_budget_bytes 250 and send three distinct 100-byte tool patterns.
//  2. Assert the request is rejected at the third tool, naming the stored limit, and that two patterns pass.
func TestPatternBudgetKnobLowersTheBudget(t *testing.T) {
	knobs := FilterKnobs{PatternBudgetBytes: pointerTo(250)}
	if err := normalizeWithCostKnobs(t, knobs, patternRequestBody(toolsWithPatterns(t, distinctFillerPatterns("a", 2, 100)...), "")); err != nil {
		t.Fatalf("200 pattern bytes under a budget of 250 = %v, want acceptance", err)
	}
	err := normalizeWithCostKnobs(t, knobs, patternRequestBody(toolsWithPatterns(t, distinctFillerPatterns("a", 3, 100)...), ""))
	requireToolsRejection(t, err, "tools[2].function.parameters: pattern budget exceeded: limit 250 bytes")
}

// Test flow:
//  1. Build ten distinct 500-byte tool patterns, 5000 bytes in all.
//  2. Assert the default profile rejects them for passing 4096 bytes, and a model storing pattern_budget_bytes at the ceiling accepts them.
//  3. Build patterns one byte past the ceiling and assert the raised model rejects them, naming the ceiling.
func TestPatternBudgetKnobRaisesTheBudgetUpToTheCeiling(t *testing.T) {
	body := patternRequestBody(toolsWithPatterns(t, distinctFillerPatterns("a", 10, 500)...), "")
	requireToolsRejection(t, normalizeWithCostKnobs(t, FilterKnobs{}, body), "pattern budget exceeded: limit 4096 bytes")
	knobs := FilterKnobs{PatternBudgetBytes: pointerTo(MaxPatternBudgetBytes)}
	if err := normalizeWithCostKnobs(t, knobs, body); err != nil {
		t.Fatalf("5000 pattern bytes under a raised budget = %v, want acceptance", err)
	}
	over := distinctFillerPatterns("b", MaxPatternBudgetBytes/500, 500)
	over = append(over, fillerPattern("c", MaxPatternBudgetBytes-len(over)*500+1))
	err := normalizeWithCostKnobs(t, knobs, patternRequestBody(toolsWithPatterns(t, over...), ""))
	requireToolsRejection(t, err, "pattern budget exceeded: limit 8192 bytes")
}

// Test flow:
//  1. Compose settings carrying each request-cost knob at zero, at a negative value, and one past its ceiling.
//  2. Assert each is refused as an invalid model setting naming the knob.
//  3. Compose each knob at 1 and at its ceiling and assert both are accepted.
func TestRequestCostKnobsRefuseValuesOutsideOneToTheCeiling(t *testing.T) {
	cases := []struct {
		name    string
		ceiling int
		set     func(value int) FilterKnobs
	}{
		{"tools_max_count", MaxToolsMaxCount, func(value int) FilterKnobs { return FilterKnobs{ToolsMaxCount: &value} }},
		{"pattern_budget_bytes", MaxPatternBudgetBytes, func(value int) FilterKnobs { return FilterKnobs{PatternBudgetBytes: &value} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, value := range []int{0, -1, testCase.ceiling + 1} {
				_, err := ComposeProfile(requestCostKnobModel, ModelSetting{Filters: testCase.set(value)})
				if !errors.Is(err, ErrInvalidModelSetting) || !strings.Contains(err.Error(), testCase.name+" "+strconv.Itoa(value)) {
					t.Errorf("ComposeProfile(%s=%d) = %v, want ErrInvalidModelSetting naming the knob and the value", testCase.name, value, err)
				}
			}
			for _, value := range []int{1, testCase.ceiling} {
				if _, err := ComposeProfile(requestCostKnobModel, ModelSetting{Filters: testCase.set(value)}); err != nil {
					t.Errorf("ComposeProfile(%s=%d) = %v, want acceptance", testCase.name, value, err)
				}
			}
		})
	}
}

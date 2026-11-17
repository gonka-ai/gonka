package filters

import "testing"

// Test flow:
//  1. Inspect `kimiProfile`'s built-in binding and Thinking mode.
//  2. Assert kimiModelID is bound to it and Thinking is ThinkingMirrorToKwargs.
//  3. Assert each boolean hook (ForceZeroPenalties, RejectStructuredOutputs, AllowSafetyIdentifier, KeepReasoningSplit, EmptyAnswerCountsTokens) matches its expected value.
func TestKimiProfileHooks(t *testing.T) {
	if BuiltInProfileFor(kimiModelID) != kimiProfile {
		t.Errorf("BuiltInProfileFor(%q) is not kimiProfile", kimiModelID)
	}
	if kimiProfile.Thinking != ThinkingMirrorToKwargs {
		t.Errorf("kimiProfile.Thinking = %v, want ThinkingMirrorToKwargs", kimiProfile.Thinking)
	}
	boolHooks := []struct {
		name string
		got  bool
		want bool
	}{
		{"ForceZeroPenalties", kimiProfile.ForceZeroPenalties, true},
		{"RejectStructuredOutputs", kimiProfile.RejectStructuredOutputs, true},
		{"AllowSafetyIdentifier", kimiProfile.AllowSafetyIdentifier, true},
		{"KeepReasoningSplit", kimiProfile.KeepReasoningSplit, false},
		{"EmptyAnswerCountsTokens", kimiProfile.EmptyAnswerCountsTokens, true},
	}
	for _, hook := range boolHooks {
		if hook.got != hook.want {
			t.Errorf("kimiProfile.%s = %v, want %v", hook.name, hook.got, hook.want)
		}
	}
}

// Test flow:
//  1. Read the thinking budget the kimi preset file declares.
//  2. Assert its numbers are the documented ones: force zero below 256, divisor 2, absolute max 96000, content headroom 64.
func TestKimiThinkingBudgetNumbers(t *testing.T) {
	want := ThinkingBudget{ForceZeroBelow: 256, DefaultDivisor: 2, AbsoluteMax: 96_000, ContentHeadroom: 64}
	if kimiProfile.ThinkingTokenBudget == nil || *kimiProfile.ThinkingTokenBudget != want {
		t.Errorf("kimi budget = %+v, want %+v", kimiProfile.ThinkingTokenBudget, want)
	}
}

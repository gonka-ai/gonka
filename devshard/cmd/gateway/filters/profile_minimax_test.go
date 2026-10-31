package filters

import "testing"

// Test flow:
//  1. Inspect `minimaxProfile`'s built-in binding and Thinking mode.
//  2. Assert minimaxModelID is bound to it and Thinking is ThinkingStrip.
//  3. Assert each boolean hook (ForceZeroPenalties, RejectStructuredOutputs, AllowSafetyIdentifier, KeepReasoningSplit, EmptyAnswerCountsTokens) matches its expected value.
func TestMinimaxProfileHooks(t *testing.T) {
	if BuiltInProfileFor(minimaxModelID) != minimaxProfile {
		t.Errorf("BuiltInProfileFor(%q) is not minimaxProfile", minimaxModelID)
	}
	if minimaxProfile.Thinking != ThinkingStrip {
		t.Errorf("minimaxProfile.Thinking = %v, want ThinkingStrip", minimaxProfile.Thinking)
	}
	boolHooks := []struct {
		name string
		got  bool
		want bool
	}{
		{"ForceZeroPenalties", minimaxProfile.ForceZeroPenalties, false},
		{"RejectStructuredOutputs", minimaxProfile.RejectStructuredOutputs, false},
		{"AllowSafetyIdentifier", minimaxProfile.AllowSafetyIdentifier, false},
		{"KeepReasoningSplit", minimaxProfile.KeepReasoningSplit, true},
		{"EmptyAnswerCountsTokens", minimaxProfile.EmptyAnswerCountsTokens, false},
	}
	for _, hook := range boolHooks {
		if hook.got != hook.want {
			t.Errorf("minimaxProfile.%s = %v, want %v", hook.name, hook.got, hook.want)
		}
	}
}

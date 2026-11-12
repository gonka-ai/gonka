package tokenizer

import (
	"strings"
	"testing"
)

const backtrackingRunes = 40

// Test flow:
//  1. Load a tokenizer whose only split pattern has no scanner and backtracks exponentially on a run of letters that ends in a mismatch.
//  2. Count such a run, and estimate it.
//  3. Assert both return an error once the match timeout passes, not a count.
func TestSplitTimeoutIsAnErrorNotACount(t *testing.T) {
	preTokenizer := map[string]any{"type": stepSequence, "pretokenizers": []any{
		map[string]any{"type": preTokenizerSplit, "pattern": map[string]any{"Regex": "(a+)+$"}, "behavior": splitIsolated, "invert": false},
		map[string]any{"type": preTokenizerByteLevel, "add_prefix_space": false, "trim_offsets": true, "use_regex": false},
	}}
	counter, err := Load(testdataWith(t, "isolated", "pre_tokenizer", preTokenizer))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a", backtrackingRunes) + "!"

	if got, err := counter.Count(text); err == nil {
		t.Errorf("Count = %d, want a timeout error", got)
	}
	if got, err := counter.EstimateTokens([]string{text}); err == nil {
		t.Errorf("EstimateTokens = %+v, want a timeout error", got)
	}
}

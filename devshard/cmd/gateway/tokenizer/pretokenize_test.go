package tokenizer

import "testing"

const patternPunctuationWordsDeepSeek = "[!\"#$%&'()*+,\\-./:;<=>?@\\[\\\\\\]^_`{|}~][A-Za-z]+|[^\r\n\\p{L}\\p{P}\\p{S}]?[\\p{L}\\p{M}]+| ?[\\p{P}\\p{S}]+[\r\n]*|\\s*[\r\n]+|\\s+(?!\\S)|\\s+"

// Test flow:
//  1. Load every testdata tokenizer and every tokenizer.json in GATEWAY_TOKENIZER_DIR.
//  2. Assert each of their split rules selected a scanner, so a pinned pattern string that drifts from the known ones fails here.
func TestEveryKnownSplitRuleSelectsAScanner(t *testing.T) {
	for _, tokenizer := range differentialTokenizers(t) {
		splits := tokenizer.reference.splits
		for index, rule := range splits {
			if rule.scan == nil {
				t.Errorf("%s: split rule %d of %d has no scanner", tokenizer.name, index+1, len(splits))
			}
		}
		t.Logf("%s: %d split rules", tokenizer.name, len(splits))
	}
}

// Test flow:
//  1. Compile the punctuation-words rule as DeepSeek-V4 writes it, with raw CR and LF and an unescaped quote.
//  2. Assert it selects a scanner instead of falling back to regexp2.
func TestTheLiteralDeepSeekPatternSelectsTheScanner(t *testing.T) {
	rule, err := compileSplit(preTokenizerSpec{Pattern: patternSpec{Regex: patternPunctuationWordsDeepSeek}, Behavior: splitIsolated})
	if err != nil {
		t.Fatal(err)
	}
	if rule.scan == nil {
		t.Fatal("the literal DeepSeek spelling fell back to regexp2")
	}
}

// Test flow:
//  1. Take every pattern string the scanners are keyed by.
//  2. Assert each is already in canonicalPattern's spelling, or the lookup could never find it.
func TestScannerPatternsAreCanonical(t *testing.T) {
	for pattern := range scanners {
		if canonical := canonicalPattern(pattern); canonical != pattern {
			t.Errorf("scanner key %q is not canonical: %q", pattern, canonical)
		}
	}
}

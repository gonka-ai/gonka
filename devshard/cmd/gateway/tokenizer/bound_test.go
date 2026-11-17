package tokenizer

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const decomposedSyllable = "\u1100\u1161\u11a8"

var boundFragments = []string{
	"the ", "quick ", "Шлюз ", "网关", "😀", "é", "ą́", "क़", "שּׁ", "\U0001d160",
	"각", "̈́", "<|im_start|>", "<｜User｜>", "<think>", "<e\u0301>", " [MASK]", "<sep> ", decomposedSyllable, "\n\n", "   ", `{"id":1,"tags":["a"]}`, "0123456789",
}

func randomBoundText(size int, rng *rand.Rand) string {
	var builder strings.Builder
	for builder.Len() < size {
		builder.WriteString(boundFragments[rng.IntN(len(boundFragments))])
	}
	text := builder.String()
	return text[:runeStartAtOrBefore(text, size)]
}

func splitIntoParts(text string, parts int) []string {
	prompt := make([]string, 0, parts)
	for part := range parts {
		start := runeStartAtOrBefore(text, len(text)*part/parts)
		end := runeStartAtOrBefore(text, len(text)*(part+1)/parts)
		prompt = append(prompt, text[start:end])
	}
	return prompt
}

// Test flow:
//  1. Load every testdata tokenizer, NFC and not, with added tokens and without, and build prompts of random ASCII, Cyrillic, CJK, emoji, special-token and added-token spellings, decomposed accents and characters NFC expands, one of only the character NFC expands most, and one of only a decomposed Hangul syllable, whose leading jamo is an added token NFC would otherwise compose, and one of only spaces, one pre-token past pieceCapBytes, from empty to past exactBudgetBytes, in one to three parts.
//  2. Take MaximumTokens and the Minimum of EstimateTokens and of EstimateTokensWithPieceCap of each.
//  3. Assert neither Minimum ever exceeds MaximumTokens.
func TestMaximumTokensBoundsTheMinimumOnEveryText(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	sizes := []int{0, 1, 200, 4 << 10, exactBudgetBytes, exactBudgetBytes + 1, 200 << 10}
	builders := []func(size int) string{
		func(size int) string { return randomBoundText(size, rng) },
		func(size int) string { return strings.Repeat("\U0001d160", size/len("\U0001d160")) },
		func(size int) string { return strings.Repeat(decomposedSyllable, size/len(decomposedSyllable)) },
		func(size int) string { return norm.NFD.String(randomBoundText(size, rng)) },
		func(size int) string { return strings.Repeat(" ", size) },
	}
	texts := make([]string, 0, len(sizes)*len(builders))
	for _, size := range sizes {
		for _, build := range builders {
			texts = append(texts, build(size))
		}
	}
	for _, name := range []string{"isolated", "ignore-merges", "removed-nfc", "sequence", "added-tokens", "added-tokens-nfc"} {
		counter := loadTestdata(t, name)
		for index, text := range texts {
			prompt := splitIntoParts(text, 1+index%3)

			maximum := counter.MaximumTokens(prompt)
			for _, estimateTokens := range []func([]string) (Estimate, error){counter.EstimateTokens, counter.EstimateTokensWithPieceCap} {
				estimate, err := estimateTokens(prompt)
				if err != nil {
					t.Fatal(err)
				}

				if estimate.Minimum() > maximum {
					t.Errorf("%s, text %d (%d bytes in %d parts): Minimum = %d (sampled %v), past MaximumTokens = %d", name, index, len(text), len(prompt), estimate.Minimum(), estimate.Sampled, maximum)
				}
			}
		}
	}
}

// Test flow:
//  1. Take every Unicode scalar value.
//  2. Normalize each to NFC.
//  3. Assert its UTF-8 grows by at most nfcMaxExpansion.
func TestNFCAtMostTriplesARunesBytes(t *testing.T) {
	for value := rune(0); value <= utf8.MaxRune; value++ {
		if !utf8.ValidRune(value) {
			continue
		}
		text := string(value)
		if normalized := norm.NFC.String(text); len(normalized) > nfcMaxExpansion*len(text) {
			t.Fatalf("NFC of %U is %d bytes, past %d times its %d", value, len(normalized), nfcMaxExpansion, len(text))
		}
	}
}

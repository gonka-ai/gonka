package tokenizer

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const addedTokenTestID = 1 << 20

var addedTokenCounts = map[string][]struct {
	text string
	want int
}{
	"added-tokens": {
		{"", 0},
		{"<think>", 1},
		{"<think>hello</think>", 3},
		{"Hello <think> world", 12},
		{"word<think>word", 9},
		{"<think><think></think>", 3},
		{"<th<think>", 2},
		{"<thin", 2},
		{"  [MASK]  tail", 6},
		{"a\n\n[MASK]", 2},
		{"x<sep>   \n y", 3},
		{"  <both>  ", 1},
		{"<|im_end|>\n<|im_start|>user", 17},
		{"grand ran ranran", 8},
		{"<n>\n<n>", 3},
		{"plain text without any", 17},
		{"end with <think>", 10},
		{"[MASK][MASK] <sep><sep>", 5},
		{"<sep> [MASK]", 2},
		{"<both> <both>", 2},
		{"\t<think>\n\n</think>\t", 6},
		{"\u041f\u0440\u0438\u0432\u0435\u0442<think>\u043c\u0438\u0440", 5},
		{"1234<sep>5678", 7},
	},
	"added-tokens-nfc": {
		{"<e\u0301>", 1},
		{"<\u00e9>", 1},
		{"<o\u0301>", 1},
		{"<\u00f3>", 4},
		{"caf\u00e9 <think> na\u00efve", 14},
		{"x [MASK]", 2},
		{"x  [MASK]y", 3},
		{"e\u0301<think>\u0301", 5},
		{"<e\u0301><o\u0301><\u00e9>", 3},
		{"<sep>  \u00e9t\u00e9", 6},
		{"plain caf\u00e9", 9},
		{"\u1100\u1161\u11a8", 7},
		{"\uac01", 3},
		{"x\u1100\u1161", 5},
	},
}

func testdataWith(t testing.TB, name, key string, value any) *bytes.Reader {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := stdjson.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document[key] = value
	edited, err := stdjson.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(edited)
}

// Test flow:
//  1. Load the testdata tokenizers that carry added tokens: one without a normalizer, one with NFC, with special and plain, lstrip, rstrip, normalized and prefix-sharing entries.
//  2. Count texts with added tokens at the start, middle and end, adjacent, inside words, around spaces and line breaks, in composed and decomposed forms, and before marks NFC composes with them.
//  3. Assert each count equals the one Hugging Face tokenizers 0.23.3 returns for the same file and text.
func TestCountSplitsOnAddedTokensAsHuggingFace(t *testing.T) {
	for name, cases := range addedTokenCounts {
		counter := loadTestdata(t, name)
		for _, test := range cases {
			got, err := counter.Count(test.text)
			if err != nil {
				t.Fatalf("%s: Count(%q) error = %v", name, test.text, err)
			}
			if got != test.want {
				t.Errorf("%s: Count(%q) = %d, want %d", name, test.text, got, test.want)
			}
		}
	}
}

// Test flow:
//  1. Load a tokenizer whose added token is single_word, whose word-character test follows the Unicode tables of Rust's regex crate.
//  2. Assert the load fails with ErrUnsupported.
func TestLoadRefusesSingleWordAddedTokens(t *testing.T) {
	singleWord := map[string]any{"id": addedTokenTestID, "content": "<w>", "single_word": true, "lstrip": false, "rstrip": false, "normalized": false, "special": true}
	document := testdataWith(t, "isolated", "added_tokens", []any{singleWord})

	if _, err := Load(document); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Load error = %v, want ErrUnsupported", err)
	}
}

package tokenizer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var goldenTexts = []string{
	"The quick brown fox jumps over the lazy dog.",
	"It's 2026, and we'll count 1234567 tokens!",
	"Привет, мир! Это проверка.",
	"你好，世界。这是一个分词器测试。",
	"func main() {\n\tfmt.Println(\"hello\")\n}\n",
	"a zebra ran quickly",
	"  leading   spaces\t\ttabs\r\n\r\nnewlines   ",
	"été café",
	"naïve 👍🏽 emoji ZWJ 👨‍👩‍👧",
}

func loadTestdata(t testing.TB, name string) *Counter {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	counter, err := Load(file)
	if err != nil {
		t.Fatalf("Load(%s) = %v", name, err)
	}
	return counter
}

// Test flow:
//  1. Load each testdata tokenizer: one per pre-tokenizer shape the pinned models use (Isolated split, ignore_merges, NFC with an inverted Removed split, a Sequence of three splits).
//  2. Count every golden text.
//  3. Assert each count equals the one Hugging Face tokenizers 0.23.3 returns for the same file and text.
func TestCountMatchesHuggingFace(t *testing.T) {
	want := map[string][]int{
		"isolated":      {10, 17, 7, 4, 10, 12, 35, 14, 44},
		"ignore-merges": {10, 17, 7, 4, 10, 6, 35, 14, 44},
		"removed-nfc":   {10, 16, 7, 4, 10, 12, 35, 11, 44},
		"sequence":      {10, 17, 7, 6, 10, 12, 35, 14, 44},
	}
	for name, counts := range want {
		counter := loadTestdata(t, name)
		for index, text := range goldenTexts {
			got, err := counter.Count(text)
			if err != nil {
				t.Fatalf("%s: Count(%q) error = %v", name, text, err)
			}
			if got != counts[index] {
				t.Errorf("%s: Count(%q) = %d, want %d", name, text, got, counts[index])
			}
		}
	}
}

// Test flow:
//  1. Load tokenizer.json documents outside the counted shape: a WordPiece model, a BPE with byte fallback, a pre-tokenizer without ByteLevel, and a Split with an unsupported behavior.
//  2. Assert each load fails with ErrUnsupported.
//  3. Assert a document that is not JSON fails without ErrUnsupported.
func TestLoadRefusesWhatItCannotCountExactly(t *testing.T) {
	byteLevel := `{"type":"ByteLevel","add_prefix_space":false,"use_regex":false}`
	cases := map[string]string{
		"wordpiece":     `{"model":{"type":"WordPiece"},"pre_tokenizer":` + byteLevel + `}`,
		"byte fallback": `{"model":{"type":"BPE","byte_fallback":true},"pre_tokenizer":` + byteLevel + `}`,
		"no byte level": `{"model":{"type":"BPE"},"pre_tokenizer":{"type":"Whitespace"}}`,
		"merged split":  `{"model":{"type":"BPE"},"pre_tokenizer":{"type":"Sequence","pretokenizers":[{"type":"Split","pattern":{"Regex":"a"},"behavior":"MergedWithPrevious"},` + byteLevel + `]}}`,
	}
	for name, document := range cases {
		if _, err := Load(strings.NewReader(document)); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: Load error = %v, want ErrUnsupported", name, err)
		}
	}
	if _, err := Load(strings.NewReader("not json")); err == nil || errors.Is(err, ErrUnsupported) {
		t.Errorf("Load(not json) error = %v, want a decode error", err)
	}
}

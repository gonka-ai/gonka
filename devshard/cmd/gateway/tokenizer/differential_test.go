package tokenizer

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

const (
	corpusDirVariable           = "GATEWAY_BENCH_CORPUS_DIR"
	sweepVariable               = "GATEWAY_TOKENIZER_SWEEP"
	differentialRandomInputs    = 150
	differentialRandomMaxBytes  = 3000
	differentialRandomRunes     = 400
	differentialLongRun         = 100 << 10
	differentialBenchBytes      = 64 << 10
	differentialPrefixCuts      = 40
	differentialMutatedInputs   = 5000
	differentialMutatedMaxParts = 16
	differentialCorpusWindow    = 48 << 10
	differentialCorpusWindows   = 6
	differentialFuzzMaxBytes    = 1 << 12
	differentialMaxReported     = 20
	sweepWorkers                = 10
	maxScalarValue              = 0x10FFFF
	surrogateFirst              = 0xD800
	surrogateLast               = 0xDFFF
	lowRuneLimit                = 0x3000
	byteValuesForRandom         = 0x100
	randomValidMaxRunRune       = 12
	randomBreakOneIn            = 4
)

var differentialTestdataNames = []string{"isolated", "ignore-merges", "removed-nfc", "sequence", "added-tokens", "added-tokens-nfc"}

var differentialNamedTexts = map[string]string{
	"empty":            "",
	"one space":        " ",
	"one letter":       "a",
	"one newline":      "\n",
	"prose":            "The quick brown fox jumps over the lazy dog. It's 2026, and we'll count 1234567 tokens! They'RE here, I'VE seen 'em; she'D go.",
	"go":               "func (s *Server) handle(w http.ResponseWriter, r *http.Request) {\n\tif err := s.run(); err != nil {\n\t\treturn\n\t}\n\tx := a<b && c>d || e&f\n}\n",
	"python":           "def f(x):\n    return {k: v for k, v in x.items() if v is not None}  # <ok> & {fine}\n\n\nclass A:\n    pass\n",
	"js":               "const f = async (a, b) => { return <div className=\"x\">{a && b}</div>; };\nconsole.log(`${a}<b>&{c}`);\n",
	"html":             "<html><body><p class=\"a\">&amp; &lt;b&gt;</p></body></html>",
	"json":             `{"id":12345678901234,"name":"item-ab12","tags":["a1","b2"],"nested":{"ok":true,"v":-1.5e10,"y":null},"html":"<b>&amp;</b>"}`,
	"russian":          "Привет, мир! Это проверка токенизатора. Ёлка и ЁЖИК, не КРИК. 1234567890.",
	"chinese":          "你好，世界。这是一个分词器测试。我们在2026年测试123456。今天天气很好，我们去公园散步吧！",
	"japanese":         "これはテストです。カタカナとひらがなと漢字、ＡＢＣ１２３。ｱｲｳ",
	"korean":           "안녕하세요 세계, 토크나이저 시험입니다.",
	"emoji":            "👍🏽 emoji 😀😀 ZWJ 👨‍👩‍👧‍👦 flag 🇺🇸🇯🇵 keycap 1️⃣ 🏳️‍🌈 heart ❤️",
	"combining":        "e\u0301 a\u0308\u0308 \u0301x \u0301\u0301 ǅ ǈ Ǆ ǆ ʰ ʻ ａ ª º ᴬ é ä ộ नि 각 Å Å ᴖ0",
	"mixed digits":     "12345678901234 ٣٤٥٦ ½ ¼ ²³ Ⅷ 1,234.567 ０１２３４ 3.14159265358979 0x1F600 1e-10 -987654321",
	"digits letters":   "abc123def 45x 6 7 8 9 123 4567 89",
	"whitespace":       "a" + strings.Repeat(" ", 50) + "b" + strings.Repeat("\t", 9) + "c" + strings.Repeat("\n", 20) + "d" + strings.Repeat(" \n", 9) + "e",
	"long whitespace":  strings.Repeat(" ", 200) + "x" + strings.Repeat("\n", 100) + "\t\t\t y" + strings.Repeat(" \n", 50),
	"newline runs":     "\n\n\n  \n \r\n\r\n \r \r\r\n  x\n\n",
	"unicode spaces":   "a\u00a0b\u2003c\u3000d\u0085e\u2028f\u2029g\u200bh \u00a0 i\u180ej\u1680k\u205fl",
	"space at end":     "word   ",
	"space before nl":  "word   \nnext   \n\n  end",
	"trailing newline": "word\n",
	"leading space":    "   leading   spaces\t\ttabs\r\n\r\nnewlines   \n\n\n",
	"quotes":           "'s 't 're 've 'm 'll 'd 'S 'T 'RE 'Ve 'M 'Ll 'D 'x ''s a's' 'ſ 'sa 's' ʼs ’s",
	"contractions":     "I'm you're we'll they'd it's don't can'T I'VE X'LL Y'D 'tis 'twas rock'n'roll",
	"punctuation":      "!#$%&'()*+,-./:;<=>?@[\\]^_`{|}~ ... --- !!! ?!?! ./foo/bar.go:12: \"quote\" `tick` ~/x",
	"punct words":      "!abc ?Def (ghi) _jkl .mno -pqr =xyz /path/to/File.GO $var @user #tag",
	"mixed case":       "camelCaseWord PascalCaseWord SCREAMINGCase HTTPServer XMLHttpRequest iPhone ǅungla ÀÉîõü ÀÉÎÕÜ",
	"cased lm lo":      "ʰello ʰELLO Ǆab ǅab ꙮ Aʰ aʰ ʰ ʰʰ ŉ ĸ ß ẞ ǰ",
	"marks only":       "\u0301\u0301\u0301 \u0301a a\u0301 \u0301A A\u0301 \u0903\u093e",
	"special tokens":   "<|im_start|>user\nhello<|im_end|>\n<|endoftext|> <s></s>[INST] <<SYS>>",
	"invalid utf8":     "a\xffb\xc3 c\xe4\xb8 d\xed\xa0\x80 e\xf4\x90\x80\x80 f\x80\x80g \xc0\xaf",
	"invalid mixed":    "a\xffb\xc0\xafc\xe2\x82d\xf0\x9f\x98e\xed\xa0\x80f\xf4\x90\x80\x80g",
	"invalid edges":    "\xff",
	"invalid run":      "\xff\xfe\xfd",
	"invalid tail":     "abc \xe4\xb8",
	"invalid head":     "\xb8abc",
	"invalid emoji":    "\xf0\x9f\x98",
	"invalid mark":     "é\xcc\x81\xcc",
	"replacement":      "\ufffd\ufffd a\ufffdb \ufffd1 \ufeff\u200b\u200d\u2028\u0000\x01\x7f",
	"nul":              "a\x00b\x00\x00 \x01\x1f",
	"long words":       "zebra ran quickly aaaaaaaaaaaaaaaaaaaa " + strings.Repeat("b", 81),
	"repeat ab":        strings.Repeat("ab", 500),
	"repeat ni":        strings.Repeat("你", 400),
	"repeat digits":    strings.Repeat("0123456789", 100),
	"equal ranks":      "lll\n}}}\nlllll}}}}}",
}

var differentialLongRuns = map[string]string{
	"run a":      strings.Repeat("a", differentialLongRun),
	"run A":      strings.Repeat("A", differentialLongRun),
	"run space":  strings.Repeat(" ", differentialLongRun),
	"run nl":     strings.Repeat("\n", differentialLongRun),
	"run nl sp":  strings.Repeat("\n ", differentialLongRun/2),
	"run dot":    strings.Repeat(".", differentialLongRun),
	"run digit":  strings.Repeat("7", differentialLongRun),
	"run zh":     strings.Repeat("中", differentialLongRun/3),
	"run emoji":  strings.Repeat("😀", differentialLongRun/4),
	"run mark":   strings.Repeat("\u0301", differentialLongRun/2),
	"run lu lo":  strings.Repeat("Aaǅ中", differentialLongRun/8),
	"run ab sp":  strings.Repeat("ab ", differentialLongRun/3),
	"run words":  strings.Repeat("the cat ", differentialLongRun/8),
	"run upper":  strings.Repeat("HTTPServer ", differentialLongRun/11),
	"run quotes": strings.Repeat("it's ", differentialLongRun/5),
}

var differentialMutationPool = []string{" ", "  ", "\n", "\r\n", "\t", "'s", "'LL", "'", "1", "12345", "a", "Ab", "AB", "é", "中", "あ", "ア", "😀", "\u200d", "\u0301", "\ufffd", "\xff", ".", "!", "!a", "-", "/", "<", "&", "ǅ", "ʰ", "\u00a0", "\u2028", "٣", "_"}

var sweepContexts = []string{"%s", "a%sb", " %sA", "'%s", "A%sa", "\n%s \n", "1%s2", "%s  x", "Ab%sc'S", "  %s", "'%sx", "'r%sx", "'v%sx", "'l%sx", "'R%sx", "'L%sx", "x'%sx"}

type differentialInput struct {
	name string
	text string
}

type differentialTokenizer struct {
	name      string
	reference *referenceCounter
}

func randomUTF8(rng *rand.Rand, runes int) string {
	var builder strings.Builder
	for range runes {
		value := rune(rng.IntN(maxScalarValue + 1))
		if value >= surrogateFirst && value <= surrogateLast {
			value = 'x'
		}
		if rng.IntN(2) == 0 {
			value = rune(rng.IntN(lowRuneLimit))
		}
		builder.WriteRune(value)
	}
	return builder.String()
}

func randomValidText(rng *rand.Rand, size int) string {
	blocks := [][2]rune{{0x20, 0x7e}, {0xa0, 0x24f}, {0x400, 0x4ff}, {0x4e00, 0x9fff}, {0x3040, 0x30ff}, {0x1f300, 0x1f64f}, {0x300, 0x36f}, {0x200b, 0x200f}, {0x0, 0x7f}, {0x10000, 0x1ffff}}
	var builder strings.Builder
	for builder.Len() < size {
		block := blocks[rng.IntN(len(blocks))]
		for range 1 + rng.IntN(randomValidMaxRunRune) {
			value := block[0] + rune(rng.IntN(int(block[1]-block[0])+1))
			if utf8.ValidRune(value) {
				builder.WriteRune(value)
			}
		}
		if rng.IntN(randomBreakOneIn) == 0 {
			builder.WriteByte(" \n\t"[rng.IntN(len(" \n\t"))])
		}
	}
	return builder.String()
}

func randomBytesText(rng *rand.Rand, size int) string {
	raw := make([]byte, size)
	for index := range raw {
		raw[index] = byte(rng.IntN(byteValuesForRandom))
	}
	return string(raw)
}

func differentialInputs(t testing.TB) []differentialInput {
	rng := rand.New(rand.NewPCG(7, 11))
	var inputs []differentialInput
	for name, text := range differentialNamedTexts {
		inputs = append(inputs, differentialInput{name, text})
		for cut := 1; cut < len(text) && cut < differentialPrefixCuts; cut++ {
			inputs = append(inputs, differentialInput{fmt.Sprintf("%s [:%d]", name, cut), text[:cut]})
		}
	}
	for name, text := range differentialLongRuns {
		inputs = append(inputs, differentialInput{name, text})
	}
	for name, text := range seedTexts {
		inputs = append(inputs, differentialInput{"seed " + name, text})
	}
	for index, text := range goldenTexts {
		inputs = append(inputs, differentialInput{fmt.Sprintf("golden %d", index), text})
	}
	for _, kind := range benchKinds {
		inputs = append(inputs, differentialInput{"bench " + kind, benchText(kind, differentialBenchBytes)})
	}
	for index := range differentialRandomInputs {
		size := 1 + rng.IntN(differentialRandomMaxBytes)
		inputs = append(inputs,
			differentialInput{fmt.Sprintf("random utf8 %d", index), randomUTF8(rng, differentialRandomRunes)},
			differentialInput{fmt.Sprintf("random valid %d", index), randomValidText(rng, size)},
			differentialInput{fmt.Sprintf("random bytes %d", index), randomBytesText(rng, size)},
		)
	}
	for index := range differentialMutatedInputs {
		var builder strings.Builder
		for range 1 + rng.IntN(differentialMutatedMaxParts) {
			builder.WriteString(differentialMutationPool[rng.IntN(len(differentialMutationPool))])
		}
		inputs = append(inputs, differentialInput{fmt.Sprintf("mutated %d", index), builder.String()})
	}
	return append(inputs, differentialCorpusInputs(t)...)
}

func differentialCorpusInputs(t testing.TB) []differentialInput {
	dir := os.Getenv(corpusDirVariable)
	if dir == "" {
		return nil
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var inputs []differentialInput
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for window := range differentialCorpusWindows {
			start := window * len(raw) / differentialCorpusWindows
			end := min(len(raw), start+differentialCorpusWindow)
			inputs = append(inputs, differentialInput{fmt.Sprintf("%s window %d", filepath.Base(path), window), string(raw[start:end])})
		}
	}
	return inputs
}

func differentialTokenizers(t testing.TB) []differentialTokenizer {
	var tokenizers []differentialTokenizer
	for _, name := range differentialTestdataNames {
		tokenizers = append(tokenizers, differentialTokenizer{name, loadReference(t, filepath.Join("testdata", name+".json"))})
	}
	dir := os.Getenv(tokenizerDirVariable)
	if dir == "" {
		return tokenizers
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		tokenizers = append(tokenizers, differentialTokenizer{strings.TrimSuffix(filepath.Base(path), ".json"), loadReference(t, path)})
	}
	return tokenizers
}

func variantPieceIDs(c *Counter, piece string, scratch *mergeScratch) []uint32 {
	switch {
	case len(piece) == 0:
		return nil
	case len(piece) == 1:
		return []uint32{c.byteSymbol[piece[0]]}
	}
	if c.whole != nil {
		if _, found := c.whole[piece]; found {
			return []uint32{originalWholeSentinel}
		}
	}
	remaining := scratch.merge(c, piece)
	if len(piece) <= scanMergeBytes {
		return slices.Clone(scratch.scan.tokens[:remaining])
	}
	ids := make([]uint32, 0, remaining)
	for index := int32(0); index != noSymbol; index = scratch.symbols[index].next {
		ids = append(ids, scratch.symbols[index].token)
	}
	return ids
}

var errHoldsAddedTokens = errors.New("text holds added tokens, which the original counts as ordinary text")

func compareWithOriginal(r *referenceCounter, text string) error {
	c := r.Counter
	wantPieces, err := r.originalPreTokens(text)
	if err != nil {
		return fmt.Errorf("original split: %w", err)
	}
	gotPieces, added, err := c.preTokens(text)
	if err != nil {
		return fmt.Errorf("split: %w", err)
	}
	if added > 0 {
		return errHoldsAddedTokens
	}
	if !slices.Equal(gotPieces, wantPieces) {
		return fmt.Errorf("pre-tokens differ for %d-byte text: got %d, want %d", len(text), len(gotPieces), len(wantPieces))
	}
	want := r.ids(wantPieces)
	var scratch mergeScratch
	var got []uint32
	for _, piece := range gotPieces {
		got = append(got, variantPieceIDs(c, piece, &scratch)...)
	}
	if !slices.Equal(got, want) {
		return fmt.Errorf("ids differ for %d-byte text: got %d ids, want %d", len(text), len(got), len(want))
	}
	for pass := range 2 {
		counted, err := c.Count(text)
		if err != nil {
			return err
		}
		if counted != len(want) {
			return fmt.Errorf("Count pass %d = %d, want %d ids for %d-byte text", pass, counted, len(want), len(text))
		}
	}
	return nil
}

// Test flow:
//  1. Load every testdata tokenizer and every tokenizer.json in GATEWAY_TOKENIZER_DIR, each with the c66c386bc implementation frozen beside it.
//  2. Build the inputs: named texts in every script and shape with their short prefixes, long runs, the seed, golden and bench texts, random UTF-8, random valid text, random bytes, mutated fragments, and windows of the corpora in GATEWAY_BENCH_CORPUS_DIR.
//  3. For each tokenizer and input that holds none of its added tokens, assert the current pre-tokens and full token ID sequence equal the original's (regexp2 split, heap merge).
//  4. Assert Count, called twice so the second call meets the pooled memo, equals the number of IDs.
func TestIDsMatchTheOriginal(t *testing.T) {
	inputs := differentialInputs(t)
	for _, tokenizer := range differentialTokenizers(t) {
		mismatches, skipped := 0, 0
		for _, input := range inputs {
			err := compareWithOriginal(tokenizer.reference, input.text)
			switch {
			case errors.Is(err, errHoldsAddedTokens):
				skipped++
			case err != nil:
				mismatches++
				if mismatches <= differentialMaxReported {
					t.Errorf("%s / %s: %v", tokenizer.name, input.name, err)
				}
			}
		}
		t.Logf("%s: %d inputs, %d with added tokens skipped, %d mismatches", tokenizer.name, len(inputs), skipped, mismatches)
	}
}

// Test flow:
//  1. Skip unless GATEWAY_TOKENIZER_SWEEP is set; it is the exhaustive sweep.
//  2. For every Unicode scalar value, build texts that put it first, last, between letters, digits, spaces and line breaks, and after contraction quotes.
//  3. For each tokenizer and text that holds none of its added tokens, assert the pre-tokens, IDs and Count equal the original's.
func TestIDsMatchTheOriginalOnEveryRune(t *testing.T) {
	if os.Getenv(sweepVariable) == "" {
		t.Skipf("set %s to sweep every rune", sweepVariable)
	}
	tokenizers := differentialTokenizers(t)
	var compared, mismatches atomic.Int64
	var group sync.WaitGroup
	for worker := range sweepWorkers {
		group.Go(func() {
			for value := rune(worker); value <= maxScalarValue; value += sweepWorkers {
				if value >= surrogateFirst && value <= surrogateLast {
					continue
				}
				for _, tokenizer := range tokenizers {
					for _, context := range sweepContexts {
						text := strings.Replace(context, "%s", string(value), 1)
						compared.Add(1)
						err := compareWithOriginal(tokenizer.reference, text)
						if err != nil && !errors.Is(err, errHoldsAddedTokens) && mismatches.Add(1) <= differentialMaxReported {
							t.Errorf("%s: U+%04X in %q: %v", tokenizer.name, value, context, err)
						}
					}
				}
			}
		})
	}
	group.Wait()
	t.Logf("%d tokenizers, %d rune texts, %d mismatches", len(tokenizers), compared.Load(), mismatches.Load())
}

var (
	fuzzTokenizersOnce sync.Once
	fuzzTokenizers     []differentialTokenizer
)

// Test flow:
//  1. Load the tokenizers once, and seed the corpus with the golden and short named texts.
//  2. For each fuzzed text and tokenizer index, skip a text that holds the tokenizer's added tokens, and compare the current pre-tokens, IDs and Count with the original's.
//  3. Fail on the first difference.
func FuzzIDsMatchTheOriginal(f *testing.F) {
	fuzzTokenizersOnce.Do(func() { fuzzTokenizers = differentialTokenizers(f) })
	for index, text := range goldenTexts {
		f.Add(text, uint8(index))
	}
	for _, text := range differentialNamedTexts {
		if len(text) <= differentialFuzzMaxBytes {
			f.Add(text, uint8(len(text)))
		}
	}
	f.Fuzz(func(t *testing.T, text string, which uint8) {
		if len(text) > differentialFuzzMaxBytes {
			t.Skip()
		}
		tokenizer := fuzzTokenizers[int(which)%len(fuzzTokenizers)]
		err := compareWithOriginal(tokenizer.reference, text)
		if errors.Is(err, errHoldsAddedTokens) {
			t.Skip()
		}
		if err != nil {
			t.Fatalf("%s: %v", tokenizer.name, err)
		}
	})
}

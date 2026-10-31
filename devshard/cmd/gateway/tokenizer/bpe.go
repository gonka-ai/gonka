package tokenizer

import (
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"math/bits"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	json "github.com/goccy/go-json"
	"golang.org/x/text/unicode/norm"
)

// ErrUnsupported marks a tokenizer.json this package does not count exactly, so its model is not checked.
var ErrUnsupported = errors.New("tokenizer not supported")

// See README.md, "Counting exactly".
const (
	byteValues         = 256
	pairKeyShift       = 32
	maxRememberedPiece = 64
	noPattern          = ""
	noAffix            = ""
	noDropout          = 0
	minMemoSlots       = 64
	maxMemoSlots       = 1 << 16
	memoPiecesPerSlot  = 2
	splitMatchTimeout  = 500 * time.Millisecond
)

var memoSeed = maphash.MakeSeed()

// Counter is one model's byte-level BPE tokenizer, kept only to count tokens. It is immutable once loaded.
type Counter struct {
	nfc                   bool
	rawAddedTokens        addedTokens
	normalizedAddedTokens addedTokens
	splits                []splitRule
	byteSymbol            [byteValues]uint32
	pairs                 *pairTable
	whole                 map[string]struct{}
}

type merge struct {
	rank   uint32
	result uint32
}

type splitRule struct {
	pattern     *regexp2.Regexp
	scan        matchFunc
	keepMatches bool
	keepGaps    bool
}

type tokenizerFile struct {
	AddedTokens  []addedTokenSpec `json:"added_tokens"`
	Normalizer   *normalizerSpec  `json:"normalizer"`
	PreTokenizer preTokenizerSpec `json:"pre_tokenizer"`
	Model        struct {
		Type                    string            `json:"type"`
		Vocab                   map[string]uint32 `json:"vocab"`
		Merges                  []mergeSpec       `json:"merges"`
		IgnoreMerges            bool              `json:"ignore_merges"`
		ByteFallback            bool              `json:"byte_fallback"`
		Dropout                 *float64          `json:"dropout"`
		ContinuingSubwordPrefix string            `json:"continuing_subword_prefix"`
		EndOfWordSuffix         string            `json:"end_of_word_suffix"`
	} `json:"model"`
}

type normalizerSpec struct {
	Type        string           `json:"type"`
	Normalizers []normalizerSpec `json:"normalizers"`
}

type preTokenizerSpec struct {
	Type           string             `json:"type"`
	PreTokenizers  []preTokenizerSpec `json:"pretokenizers"`
	Pattern        patternSpec        `json:"pattern"`
	Behavior       string             `json:"behavior"`
	Invert         bool               `json:"invert"`
	AddPrefixSpace bool               `json:"add_prefix_space"`
	UseRegex       bool               `json:"use_regex"`
}

type patternSpec struct {
	Regex  string `json:"Regex"`
	String string `json:"String"`
}

type mergeSpec [2]string

func (m *mergeSpec) UnmarshalJSON(data []byte) error {
	var pair []string
	if err := json.Unmarshal(data, &pair); err == nil {
		if len(pair) != len(mergeSpec{}) {
			return fmt.Errorf("merge %s is not a pair", data)
		}
		*m = mergeSpec{pair[0], pair[1]}
		return nil
	}
	var joined string
	if err := json.Unmarshal(data, &joined); err != nil {
		return err
	}
	left, right, found := strings.Cut(joined, " ")
	if !found {
		return fmt.Errorf("merge %q has no separator", joined)
	}
	*m = mergeSpec{left, right}
	return nil
}

// Load reads a Hugging Face tokenizer.json; a file outside the byte-level BPE shape this package counts is ErrUnsupported.
func Load(r io.Reader) (*Counter, error) {
	var file tokenizerFile
	if err := json.NewDecoder(r).Decode(&file); err != nil {
		return nil, fmt.Errorf("decode tokenizer.json: %w", err)
	}
	model := file.Model
	switch {
	case model.Type != modelTypeBPE:
		return nil, fmt.Errorf("%w: model type %q", ErrUnsupported, model.Type)
	case model.ByteFallback, model.Dropout != nil && *model.Dropout != noDropout, model.ContinuingSubwordPrefix != noAffix, model.EndOfWordSuffix != noAffix:
		return nil, fmt.Errorf("%w: BPE options beyond byte-level merges", ErrUnsupported)
	}
	counter := &Counter{pairs: newPairTable(len(model.Merges))}
	var err error
	if counter.nfc, err = normalizes(file.Normalizer); err != nil {
		return nil, err
	}
	if counter.rawAddedTokens, counter.normalizedAddedTokens, err = addedTokenSets(file.AddedTokens, counter.nfc); err != nil {
		return nil, err
	}
	if counter.splits, err = splitRules(file.PreTokenizer); err != nil {
		return nil, err
	}
	symbols := byteSymbols()
	for value, symbol := range symbols {
		id, found := model.Vocab[symbol]
		if !found {
			return nil, fmt.Errorf("%w: byte %#x has no token", ErrUnsupported, value)
		}
		counter.byteSymbol[value] = id
	}
	for rank, pair := range model.Merges {
		left, leftFound := model.Vocab[pair[0]]
		right, rightFound := model.Vocab[pair[1]]
		result, resultFound := model.Vocab[pair[0]+pair[1]]
		if !leftFound || !rightFound || !resultFound {
			return nil, fmt.Errorf("merge %d (%q %q) names a token the vocabulary lacks", rank, pair[0], pair[1])
		}
		key := pairKey(left, right)
		if _, seen := counter.pairs.lookup(left, right); !seen {
			counter.pairs.add(key, merge{rank: uint32(rank), result: result})
		}
	}
	if model.IgnoreMerges {
		counter.whole = wholeTokens(model.Vocab, symbols)
	}
	return counter, nil
}

func normalizes(spec *normalizerSpec) (nfc bool, err error) {
	if spec == nil {
		return false, nil
	}
	switch spec.Type {
	case normalizerNFC:
		return true, nil
	case stepSequence:
		for index := range spec.Normalizers {
			inner, err := normalizes(&spec.Normalizers[index])
			if err != nil {
				return false, err
			}
			nfc = nfc || inner
		}
		return nfc, nil
	}
	return false, fmt.Errorf("%w: normalizer %q", ErrUnsupported, spec.Type)
}

func splitRules(spec preTokenizerSpec) ([]splitRule, error) {
	steps := []preTokenizerSpec{spec}
	if spec.Type == stepSequence {
		steps = spec.PreTokenizers
	}
	var rules []splitRule
	byteLevel := false
	for _, step := range steps {
		switch step.Type {
		case preTokenizerByteLevel:
			if step.UseRegex || step.AddPrefixSpace {
				return nil, fmt.Errorf("%w: ByteLevel with use_regex or add_prefix_space", ErrUnsupported)
			}
			byteLevel = true
		case preTokenizerSplit:
			rule, err := compileSplit(step)
			if err != nil {
				return nil, err
			}
			rules = append(rules, rule)
		default:
			return nil, fmt.Errorf("%w: pre-tokenizer %q", ErrUnsupported, step.Type)
		}
	}
	if !byteLevel {
		return nil, fmt.Errorf("%w: no ByteLevel pre-tokenizer", ErrUnsupported)
	}
	return rules, nil
}

func compileSplit(step preTokenizerSpec) (splitRule, error) {
	expression := step.Pattern.Regex
	if expression == noPattern {
		expression = regexp2.Escape(step.Pattern.String)
	}
	if expression == noPattern {
		return splitRule{}, fmt.Errorf("%w: Split without a pattern", ErrUnsupported)
	}
	pattern, err := regexp2.Compile(expression, regexp2.None)
	if err != nil {
		return splitRule{}, fmt.Errorf("compile split pattern: %w", err)
	}
	pattern.MatchTimeout = splitMatchTimeout
	scan := scanners[canonicalPattern(expression)]
	if scan != nil {
		fillBasicPlaneClasses()
	}
	switch {
	case step.Behavior == splitIsolated:
		return splitRule{pattern: pattern, scan: scan, keepMatches: true, keepGaps: true}, nil
	case step.Behavior == splitRemoved && step.Invert:
		return splitRule{pattern: pattern, scan: scan, keepMatches: true}, nil
	case step.Behavior == splitRemoved:
		return splitRule{pattern: pattern, scan: scan, keepGaps: true}, nil
	}
	return splitRule{}, fmt.Errorf("%w: split behavior %q", ErrUnsupported, step.Behavior)
}

func wholeTokens(vocab map[string]uint32, symbols [byteValues]string) map[string]struct{} {
	byteOf := make(map[rune]byte, len(symbols))
	for value, symbol := range symbols {
		symbolRune, _ := utf8.DecodeRuneInString(symbol)
		byteOf[symbolRune] = byte(value)
	}
	whole := make(map[string]struct{}, len(vocab))
	var raw []byte
	for token := range vocab {
		raw = raw[:0]
		decoded := true
		for _, symbolRune := range token {
			value, found := byteOf[symbolRune]
			if !found {
				decoded = false
				break
			}
			raw = append(raw, value)
		}
		if decoded {
			whole[string(raw)] = struct{}{}
		}
	}
	return whole
}

func byteSymbols() [byteValues]string {
	var symbols [byteValues]string
	shifted := 0
	for value := range byteValues {
		switch {
		case value >= '!' && value <= '~', value >= '¡' && value <= '¬', value >= '®':
			symbols[value] = string(rune(value))
		default:
			symbols[value] = string(rune(byteValues + shifted))
			shifted++
		}
	}
	return symbols
}

func pairKey(left, right uint32) uint64 { return uint64(left)<<pairKeyShift | uint64(right) }

// Count returns text's exact token count, without the special tokens a chat template adds; an added token's content in text is one token.
func (c *Counter) Count(text string) (int, error) {
	tokens, _, err := c.count(text, uncappedPieceBytes)
	return tokens, err
}

func (c *Counter) count(text string, pieceCap int) (tokens int, capped bool, err error) {
	pieces, added, err := c.preTokens(text)
	if err != nil {
		return 0, false, err
	}
	tokens, capped = c.countPieces(pieces, pieceCap)
	return added + tokens, capped, nil
}

func (c *Counter) countPieces(pieces []string, pieceCap int) (tokens int, capped bool) {
	var scratch mergeScratch
	memo := takeMemo(memoSlots(len(pieces)))
	defer giveMemo(memo)
	for _, piece := range pieces {
		switch {
		case len(piece) > pieceCap:
			tokens += c.countCappedPiece(piece, pieceCap, &scratch)
			capped = true
		case len(piece) > maxRememberedPiece:
			tokens += c.countPiece(piece, &scratch)
		default:
			slot := &memo.slots[maphash.String(memoSeed, piece)&uint64(len(memo.slots)-1)]
			if slot.piece != piece {
				slot.piece, slot.tokens = piece, c.countPiece(piece, &scratch)
			}
			tokens += slot.tokens
		}
	}
	return tokens, capped
}

// memoTables holds cleared memo tables between calls, so a call does not allocate and zero one anew.
var memoTables sync.Pool

type memoTable struct{ slots []memoSlot }

func takeMemo(slots int) *memoTable {
	if pooled, ok := memoTables.Get().(*memoTable); ok && cap(pooled.slots) >= slots {
		pooled.slots = pooled.slots[:slots]
		return pooled
	}
	return &memoTable{slots: make([]memoSlot, slots)}
}

func giveMemo(table *memoTable) {
	clear(table.slots)
	memoTables.Put(table)
}

// memoSlot remembers the last piece that hashed to it; an empty slot holds the empty piece, which is zero tokens.
type memoSlot struct {
	piece  string
	tokens int
}

func memoSlots(pieces int) int {
	return min(maxMemoSlots, max(minMemoSlots, 1<<bits.Len(uint(pieces/memoPiecesPerSlot))))
}

// preTokens is the text cut at its added tokens, each gap normalized and cut at its normalized added tokens, then cut by every split rule in turn; added is how many added tokens it held.
func (c *Counter) preTokens(text string) (pieces []string, added int, err error) {
	gaps, added := c.rawAddedTokens.split(text, nil)
	pieces = make([]string, 0, len(gaps))
	for _, gap := range gaps {
		if c.nfc {
			gap = norm.NFC.String(gap)
		}
		var found int
		pieces, found = c.normalizedAddedTokens.split(gap, pieces)
		added += found
	}
	var runes []rune
	for _, rule := range c.splits {
		next := make([]string, 0, len(pieces))
		for _, piece := range pieces {
			next, runes, err = rule.split(next, piece, runes)
			if err != nil {
				return nil, 0, err
			}
		}
		pieces = next
	}
	return pieces, added, nil
}

func (r splitRule) split(into []string, piece string, runes []rune) ([]string, []rune, error) {
	if r.scan != nil {
		return r.scanSplit(into, piece), runes, nil
	}
	runes = runes[:0]
	for _, value := range piece {
		runes = append(runes, value)
	}
	runeAt, byteAt := 0, 0
	advance := func(to int) int {
		for ; runeAt < to; runeAt++ {
			_, width := utf8.DecodeRuneInString(piece[byteAt:])
			byteAt += width
		}
		return byteAt
	}
	gapStart := 0
	match, err := r.pattern.FindRunesMatch(runes)
	for ; err == nil && match != nil; match, err = r.pattern.FindNextMatch(match) {
		if match.Length == 0 {
			continue
		}
		start := advance(match.Index)
		end := advance(match.Index + match.Length)
		if r.keepGaps && start > gapStart {
			into = append(into, piece[gapStart:start])
		}
		if r.keepMatches {
			into = append(into, piece[start:end])
		}
		gapStart = end
	}
	if err != nil {
		return into, runes, fmt.Errorf("split pre-tokens: %w", err)
	}
	if r.keepGaps && gapStart < len(piece) {
		into = append(into, piece[gapStart:])
	}
	return into, runes, nil
}

func (c *Counter) countPiece(piece string, scratch *mergeScratch) int {
	if len(piece) <= 1 {
		return len(piece)
	}
	if c.whole != nil {
		if _, found := c.whole[piece]; found {
			return 1
		}
	}
	return scratch.merge(c, piece)
}

func (c *Counter) countCappedPiece(piece string, pieceCap int, scratch *mergeScratch) int {
	prefix := piece[:runeStartAtOrBefore(piece, pieceCap)]
	if prefix == "" {
		prefix = piece[:pieceCap]
	}
	return (c.countPiece(prefix, scratch)*len(piece) + len(prefix)/2) / len(prefix)
}

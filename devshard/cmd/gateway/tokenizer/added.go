package tokenizer

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// See README.md, "Added tokens".
const (
	trieRoot     = 0
	trieByteBits = 8
	noAddedToken = -1
)

type addedTokenSpec struct {
	Content    string `json:"content"`
	SingleWord bool   `json:"single_word"`
	StripLeft  bool   `json:"lstrip"`
	StripRight bool   `json:"rstrip"`
	Normalized bool   `json:"normalized"`
}

// addedTokens finds one set of added tokens in a text, leftmost first and longest at the same start, as Hugging Face's Aho-Corasick trie does.
type addedTokens struct {
	starts   [byteValues]bool
	children map[uint64]int32
	ends     []int32
	strips   []addedStrip
}

type addedStrip struct {
	left  bool
	right bool
}

// addedTokenSets splits the entries as Hugging Face does: raw ones match the text before normalization, normalized ones match each normalized gap by their normalized content.
func addedTokenSets(specs []addedTokenSpec, nfc bool) (raw, normalized addedTokens, err error) {
	byContent := make(map[string]addedTokenSpec, len(specs))
	var order []string
	for _, spec := range specs {
		switch {
		case len(spec.Content) == 0:
			continue
		case spec.SingleWord:
			return raw, normalized, fmt.Errorf("%w: single_word added token %q", ErrUnsupported, spec.Content)
		}
		if _, seen := byContent[spec.Content]; !seen {
			order = append(order, spec.Content)
		}
		byContent[spec.Content] = spec
	}
	raw, normalized = newAddedTokens(), newAddedTokens()
	for _, content := range order {
		spec := byContent[content]
		strip := addedStrip{left: spec.StripLeft, right: spec.StripRight}
		switch {
		case !spec.Normalized:
			raw.add(content, strip)
			continue
		case nfc:
			content = norm.NFC.String(content)
		}
		if !normalized.add(content, strip) {
			return raw, normalized, fmt.Errorf("%w: added tokens normalize to the same %q", ErrUnsupported, content)
		}
	}
	return raw, normalized, nil
}

func newAddedTokens() addedTokens {
	return addedTokens{children: map[uint64]int32{}, ends: []int32{noAddedToken}}
}

// add reports false when content is already in the set.
func (a *addedTokens) add(content string, strip addedStrip) bool {
	a.starts[content[0]] = true
	node := int32(trieRoot)
	for index := range len(content) {
		key := trieKey(node, content[index])
		child, found := a.children[key]
		if !found {
			child = int32(len(a.ends))
			a.children[key] = child
			a.ends = append(a.ends, noAddedToken)
		}
		node = child
	}
	if a.ends[node] != noAddedToken {
		return false
	}
	a.ends[node] = int32(len(a.strips))
	a.strips = append(a.strips, strip)
	return true
}

func trieKey(node int32, value byte) uint64 { return uint64(node)<<trieByteBits | uint64(value) }

// next finds the leftmost added token at or after from, the longest of those starting there.
func (a *addedTokens) next(text string, from int) (start, end int, token int32) {
	for start = from; start < len(text); start++ {
		if !a.starts[text[start]] {
			continue
		}
		end, token = start, noAddedToken
		node := int32(trieRoot)
		for at := start; at < len(text); at++ {
			child, found := a.children[trieKey(node, text[at])]
			if !found {
				break
			}
			node = child
			if a.ends[node] != noAddedToken {
				end, token = at+1, a.ends[node]
			}
		}
		if token != noAddedToken {
			return start, end, token
		}
	}
	return len(text), len(text), noAddedToken
}

// split appends the text between added tokens to gaps and returns how many added tokens it held, widening a match over the spaces lstrip and rstrip take as Hugging Face's find_matches does.
func (a *addedTokens) split(text string, gaps []string) ([]string, int) {
	if len(a.strips) == 0 {
		return appendGap(gaps, text), 0
	}
	found, gapStart := 0, 0
	for from := 0; ; {
		start, end, token := a.next(text, from)
		if token == noAddedToken {
			break
		}
		from = end
		if a.strips[token].left {
			start = max(trailingSpaceStart(text[:start]), gapStart)
		}
		if a.strips[token].right {
			end += leadingSpaceEnd(text[end:])
		}
		if gapStart < start {
			gaps = append(gaps, text[gapStart:start])
		}
		found++
		gapStart = end
	}
	return appendGap(gaps, text[gapStart:]), found
}

func appendGap(gaps []string, gap string) []string {
	if len(gap) == 0 {
		return gaps
	}
	return append(gaps, gap)
}

// trailingSpaceStart is where text's trailing run of Unicode White_Space starts, Rust regex `\s*$`.
func trailingSpaceStart(text string) int {
	end := len(text)
	for end > 0 {
		value, width := utf8.DecodeLastRuneInString(text[:end])
		if !unicode.IsSpace(value) {
			break
		}
		end -= width
	}
	return end
}

// leadingSpaceEnd is where text's leading run of Unicode White_Space ends, Rust regex `^\s*`.
func leadingSpaceEnd(text string) int {
	start := 0
	for start < len(text) {
		value, width := utf8.DecodeRuneInString(text[start:])
		if !unicode.IsSpace(value) {
			break
		}
		start += width
	}
	return start
}

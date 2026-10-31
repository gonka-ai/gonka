package tokenizer

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	originalNoSymbol      = -1
	originalHeapArity     = 2
	originalWholeSentinel = ^uint32(0)
)

type originalScratch struct {
	merges     map[uint64]merge
	symbols    []originalSymbol
	candidates []originalCandidate
}

type originalSymbol struct {
	token      uint32
	prev, next int32
	version    uint32
	merged     bool
}

type originalCandidate struct {
	rank    uint32
	left    int32
	version uint32
}

func (s *originalScratch) merge(c *Counter, piece string) int {
	s.symbols = s.symbols[:0]
	for index := range len(piece) {
		s.symbols = append(s.symbols, originalSymbol{token: c.byteSymbol[piece[index]], prev: int32(index - 1), next: int32(index + 1)})
	}
	s.symbols[len(s.symbols)-1].next = originalNoSymbol
	s.candidates = s.candidates[:0]
	for left := range int32(len(s.symbols) - 1) {
		if found, ok := s.merges[pairKey(s.symbols[left].token, s.symbols[left+1].token)]; ok {
			s.candidates = append(s.candidates, originalCandidate{rank: found.rank, left: left})
		}
	}
	for parent := len(s.candidates)/originalHeapArity - 1; parent >= 0; parent-- {
		s.siftDown(parent)
	}
	remaining := len(s.symbols)
	for len(s.candidates) > 0 {
		next := s.pop()
		left := &s.symbols[next.left]
		if left.merged || left.version != next.version || left.next == originalNoSymbol {
			continue
		}
		right := &s.symbols[left.next]
		found, ok := s.merges[pairKey(left.token, right.token)]
		if !ok || found.rank != next.rank {
			continue
		}
		left.token = found.result
		left.version++
		right.merged = true
		left.next = right.next
		if right.next != originalNoSymbol {
			s.symbols[right.next].prev = next.left
		}
		remaining--
		if left.prev != originalNoSymbol {
			s.symbols[left.prev].version++
			s.offer(c, left.prev)
		}
		s.offer(c, next.left)
	}
	return remaining
}

func (s *originalScratch) offer(c *Counter, left int32) {
	current := s.symbols[left]
	if current.next == originalNoSymbol {
		return
	}
	found, ok := s.merges[pairKey(current.token, s.symbols[current.next].token)]
	if !ok {
		return
	}
	s.push(originalCandidate{rank: found.rank, left: left, version: current.version})
}

func (a originalCandidate) before(b originalCandidate) bool {
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	return a.left < b.left
}

func (s *originalScratch) push(entry originalCandidate) {
	s.candidates = append(s.candidates, entry)
	child := len(s.candidates) - 1
	for child > 0 {
		parent := (child - 1) / originalHeapArity
		if !s.candidates[child].before(s.candidates[parent]) {
			break
		}
		s.candidates[child], s.candidates[parent] = s.candidates[parent], s.candidates[child]
		child = parent
	}
}

func (s *originalScratch) pop() originalCandidate {
	top := s.candidates[0]
	last := len(s.candidates) - 1
	s.candidates[0] = s.candidates[last]
	s.candidates = s.candidates[:last]
	s.siftDown(0)
	return top
}

func (s *originalScratch) siftDown(parent int) {
	size := len(s.candidates)
	for {
		smallest, left, right := parent, originalHeapArity*parent+1, originalHeapArity*parent+originalHeapArity
		if left < size && s.candidates[left].before(s.candidates[smallest]) {
			smallest = left
		}
		if right < size && s.candidates[right].before(s.candidates[smallest]) {
			smallest = right
		}
		if smallest == parent {
			return
		}
		s.candidates[parent], s.candidates[smallest] = s.candidates[smallest], s.candidates[parent]
		parent = smallest
	}
}

type referenceCounter struct {
	*Counter
	merges map[uint64]merge
}

func loadReference(tb testing.TB, path string) *referenceCounter {
	tb.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	counter, err := Load(bytes.NewReader(raw))
	if err != nil {
		tb.Fatalf("Load(%s) = %v", path, err)
	}
	var file struct {
		Model struct {
			Vocab  map[string]uint32    `json:"vocab"`
			Merges []stdjson.RawMessage `json:"merges"`
		} `json:"model"`
	}
	if err := stdjson.Unmarshal(raw, &file); err != nil {
		tb.Fatal(err)
	}
	merges := make(map[uint64]merge, len(file.Model.Merges))
	for rank, entry := range file.Model.Merges {
		var pair [2]string
		var joined string
		if stdjson.Unmarshal(entry, &pair) != nil {
			if err := stdjson.Unmarshal(entry, &joined); err != nil {
				tb.Fatal(err)
			}
			pair[0], pair[1], _ = strings.Cut(joined, " ")
		}
		key := pairKey(file.Model.Vocab[pair[0]], file.Model.Vocab[pair[1]])
		if _, seen := merges[key]; !seen {
			merges[key] = merge{rank: uint32(rank), result: file.Model.Vocab[pair[0]+pair[1]]}
		}
	}
	return &referenceCounter{Counter: counter, merges: merges}
}

func (r *referenceCounter) pieceIDs(piece string, scratch *originalScratch) []uint32 {
	c := r.Counter
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
	ids := make([]uint32, 0, remaining)
	for index := int32(0); index != originalNoSymbol; index = scratch.symbols[index].next {
		ids = append(ids, scratch.symbols[index].token)
	}
	if len(ids) != remaining {
		panic(fmt.Sprintf("reference walk found %d symbols, merge reported %d", len(ids), remaining))
	}
	return ids
}

func (r *referenceCounter) originalPreTokens(text string) ([]string, error) {
	c := r.Counter
	if c.nfc {
		text = norm.NFC.String(text)
	}
	pieces := []string{text}
	var runes []rune
	for _, rule := range c.splits {
		next := make([]string, 0, len(pieces))
		for _, piece := range pieces {
			var err error
			next, runes, err = originalSplit(rule, next, piece, runes)
			if err != nil {
				return nil, err
			}
		}
		pieces = next
	}
	return pieces, nil
}

func (r *referenceCounter) ids(pieces []string) []uint32 {
	scratch := originalScratch{merges: r.merges}
	var ids []uint32
	for _, piece := range pieces {
		ids = append(ids, r.pieceIDs(piece, &scratch)...)
	}
	return ids
}

func originalSplit(r splitRule, into []string, piece string, runes []rune) ([]string, []rune, error) {
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

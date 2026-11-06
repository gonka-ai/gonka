package tokenizer

import (
	"math"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// See README.md, "Estimating a long prompt", "Capping a piece's merge" and "The byte bound".
const (
	exactBudgetBytes        = 64 << 10
	sampleWindows           = 8
	windowBytes             = exactBudgetBytes / sampleWindows
	sampledOvercountPercent = 7
	wholePercent            = 100
	pieceCapBytes           = 512
	uncappedPieceBytes      = math.MaxInt
	nfcMaxExpansion         = 3
)

// Estimate is one count of a prompt's tokens: exact, or estimated from sampled windows or from a capped pre-token's start, Sampled then true.
type Estimate struct {
	Tokens  uint64
	Sampled bool
}

// EstimateTokens returns prompt's tokens, each part counted on its own: exact up to exactBudgetBytes in all, the sampled estimate beyond.
func (c *Counter) EstimateTokens(prompt []string) (Estimate, error) {
	return c.estimateTokens(prompt, uncappedPieceBytes)
}

// EstimateTokensWithPieceCap is EstimateTokens with every pre-token longer than pieceCapBytes counted from its first pieceCapBytes, scaled to its length, Sampled then true.
func (c *Counter) EstimateTokensWithPieceCap(prompt []string) (Estimate, error) {
	return c.estimateTokens(prompt, pieceCapBytes)
}

func (c *Counter) estimateTokens(prompt []string, pieceCap int) (Estimate, error) {
	tokens, sampled, err := c.estimate(prompt, pieceCap)
	return Estimate{Tokens: tokens, Sampled: sampled}, err
}

// Minimum returns the fewest tokens the estimated prompt holds: the exact count, or the sampled estimate less its worst measured over-count.
func (e Estimate) Minimum() uint64 {
	if !e.Sampled {
		return e.Tokens
	}
	return e.Tokens * wholePercent / (wholePercent + sampledOvercountPercent)
}

// MaximumTokens returns a count EstimateTokens' Minimum never exceeds, read from prompt's bytes without tokenizing: every token covers at least one byte of the text it counts.
func (c *Counter) MaximumTokens(prompt []string) uint64 {
	total := 0
	for _, part := range prompt {
		total += len(part)
	}
	switch {
	case !c.nfc:
		return uint64(total)
	case total > exactBudgetBytes:
		for _, part := range prompt {
			if !isASCII(part) {
				return uint64(total) * nfcMaxExpansion
			}
		}
		return uint64(total)
	}
	normalized := 0
	for _, part := range prompt {
		normalized += c.normalizedBytes(part)
	}
	return uint64(normalized)
}

func (c *Counter) normalizedBytes(text string) int {
	gaps, _ := c.rawAddedTokens.split(text, nil)
	total := len(text)
	for _, gap := range gaps {
		total += len(norm.NFC.String(gap)) - len(gap)
	}
	return total
}

func isASCII(text string) bool {
	for index := range len(text) {
		if text[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (c *Counter) estimate(prompt []string, pieceCap int) (tokens uint64, sampled bool, err error) {
	total := 0
	for _, part := range prompt {
		total += len(part)
	}
	if total <= exactBudgetBytes {
		tokens, _, sampled, err = c.countRange(prompt, 0, total, pieceCap)
		return tokens, sampled, err
	}
	span := total / sampleWindows
	var sampledTokens, sampledBytes uint64
	for window := range sampleWindows {
		start := windowStart(window, span)
		windowTokens, windowBytesCounted, _, err := c.countRange(prompt, start, start+windowBytes, pieceCap)
		if err != nil {
			return 0, true, err
		}
		sampledTokens += windowTokens
		sampledBytes += windowBytesCounted
	}
	return (sampledTokens*uint64(total) + sampledBytes/2) / max(sampledBytes, 1), true, nil
}

// windowStart places window inside its span-byte eighth: the first at the eighth's start, the last ending at its end, the others evenly between.
func windowStart(window, span int) int {
	return window*span + window*(span-windowBytes)/(sampleWindows-1)
}

func (c *Counter) countRange(prompt []string, start, end, pieceCap int) (tokens, bytesCounted uint64, capped bool, err error) {
	offset := 0
	for _, part := range prompt {
		partStart, partEnd := max(start-offset, 0), min(end-offset, len(part))
		offset += len(part)
		partStart, partEnd = runeStartAtOrAfter(part, partStart), runeStartAtOrBefore(part, partEnd)
		if partStart >= partEnd {
			continue
		}
		segment := part[partStart:partEnd]
		segmentTokens, segmentCapped, err := c.count(segment, pieceCap)
		if err != nil {
			return 0, 0, false, err
		}
		tokens += uint64(segmentTokens)
		bytesCounted += uint64(len(segment))
		capped = capped || segmentCapped
	}
	return tokens, bytesCounted, capped, nil
}

func runeStartAtOrAfter(text string, index int) int {
	for index < len(text) && !utf8.RuneStart(text[index]) {
		index++
	}
	return index
}

func runeStartAtOrBefore(text string, index int) int {
	for index > 0 && index < len(text) && !utf8.RuneStart(text[index]) {
		index--
	}
	return index
}

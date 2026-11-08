package tokenizer

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The split patterns scanners reproduce, in canonicalPattern's spelling, and the characters they name; any other pattern runs on regexp2. See README.md, "Counting exactly".
const (
	patternLlamaDigits3     = `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`
	patternLlamaDigit1      = `(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`
	patternCasedWords       = `[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?|[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n/]*|\s*[\r\n]+|\s+(?!\S)|\s+`
	patternDigits3          = `\p{N}{1,3}`
	patternCJK              = "[一-龥぀-ゟ゠-ヿ]+"
	patternPunctuationWords = "[!\"#$%&'()*+,\\-./:;<=>?@\\[\\\\\\]^_`{|}~][A-Za-z]+|[^\\r\\n\\p{L}\\p{P}\\p{S}]?[\\p{L}\\p{M}]+| ?[\\p{P}\\p{S}]+[\\r\\n]*|\\s*[\\r\\n]+|\\s+(?!\\S)|\\s+"

	noMatch           = -1
	maxGroupedDigits  = 3
	singleDigit       = 1
	endOfText         = 0
	asciiPunctuation  = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	lineBreaks        = "\r\n"
	lineBreaksSlash   = "\r\n/"
	cjkFirstIdeograph = '一'
	cjkLastIdeograph  = '龥'
	hiraganaFirst     = '぀'
	hiraganaLast      = 'ゟ'
	katakanaFirst     = '゠'
	katakanaLast      = 'ヿ'
)

// canonicalPattern spells raw CR and LF as \r and \n and an escaped quote as a bare one: DeepSeek-V4 writes its rule with
// raw line breaks where the other tokenizers escape them. Without the x option, which no known pattern sets, regexp2 reads both alike.
func canonicalPattern(expression string) string {
	var canonical strings.Builder
	for index := 0; index < len(expression); index++ {
		switch value := expression[index]; {
		case value == '\\' && index+1 < len(expression):
			index++
			if expression[index] != '"' {
				canonical.WriteByte(value)
			}
			canonical.WriteByte(expression[index])
		case value == '\r':
			canonical.WriteString(`\r`)
		case value == '\n':
			canonical.WriteString(`\n`)
		default:
			canonical.WriteByte(value)
		}
	}
	return canonical.String()
}

// matchFunc returns the end of the pattern's match at byte offset at, or noMatch.
type matchFunc func(text string, at int) int

var scanners = map[string]matchFunc{
	patternLlamaDigits3:     llamaMatcher(maxGroupedDigits),
	patternLlamaDigit1:      llamaMatcher(singleDigit),
	patternCasedWords:       casedWordsMatch,
	patternDigits3:          func(text string, at int) int { return digitsEnd(text, at, maxGroupedDigits) },
	patternCJK:              cjkEnd,
	patternPunctuationWords: punctuationWordsMatch,
}

type classFlags uint16

// The Unicode classes the known patterns name, as regexp2 reads them: \s is unicode.IsSpace, \p{X} is unicode.Is of X.
const (
	classLetter classFlags = 1 << iota
	classNumber
	classSpace
	classLineBreak
	classUpper
	classTitle
	classModifier
	classOtherLetter
	classLower
	classMark
	classPunctuation
	classSymbol
	classCapital    = classUpper | classTitle | classModifier | classOtherLetter | classMark
	classSmall      = classLower | classModifier | classOtherLetter | classMark
	basicPlaneRunes = 1 << 16
)

var (
	asciiClasses = func() (table [utf8.RuneSelf]classFlags) {
		for value := range table {
			table[value] = classify(rune(value))
		}
		return table
	}()
	basicPlaneClasses [basicPlaneRunes]classFlags
	basicPlaneOnce    sync.Once
)

// fillBasicPlaneClasses fills the table peek reads; compileSplit calls it before any scanner exists.
func fillBasicPlaneClasses() {
	basicPlaneOnce.Do(func() {
		for value := range basicPlaneClasses {
			basicPlaneClasses[value] = classify(rune(value))
		}
	})
}

func classify(r rune) classFlags {
	var flags classFlags
	if unicode.IsSpace(r) {
		flags |= classSpace
		if r == '\r' || r == '\n' {
			flags |= classLineBreak
		}
	}
	switch {
	case unicode.IsLetter(r):
		flags |= classLetter
		switch {
		case unicode.IsLower(r):
			flags |= classLower
		case unicode.IsUpper(r):
			flags |= classUpper
		case unicode.IsTitle(r):
			flags |= classTitle
		case unicode.Is(unicode.Lm, r):
			flags |= classModifier
		default:
			flags |= classOtherLetter
		}
	case unicode.IsNumber(r):
		flags |= classNumber
	case unicode.IsMark(r):
		flags |= classMark
	case unicode.IsPunct(r):
		flags |= classPunctuation
	case unicode.IsSymbol(r):
		flags |= classSymbol
	}
	return flags
}

// peek reads the rune at byte offset at; an invalid byte is U+FFFD one byte wide, as regexp2 sees it, and the end of text has width endOfText.
func peek(text string, at int) (rune, classFlags, int) {
	if at >= len(text) {
		return 0, 0, endOfText
	}
	if value := text[at]; value < utf8.RuneSelf {
		return rune(value), asciiClasses[value], 1
	}
	r, width := utf8.DecodeRuneInString(text[at:])
	if r < basicPlaneRunes {
		return r, basicPlaneClasses[r], width
	}
	return r, classify(r), width
}

func runEnd(text string, at int, mask classFlags) int {
	for {
		_, flags, width := peek(text, at)
		if width == endOfText || flags&mask == 0 {
			return at
		}
		at += width
	}
}

func runEndAbsent(text string, at int, mask classFlags) int {
	for {
		_, flags, width := peek(text, at)
		if width == endOfText || flags&mask != 0 {
			return at
		}
		at += width
	}
}

func llamaMatcher(digits int) matchFunc {
	return func(text string, at int) int {
		if end := contractionEnd(text, at); end != noMatch {
			return end
		}
		if end := prefixed(text, at, classLineBreak|classLetter|classNumber, letterRunEnd); end != noMatch {
			return end
		}
		if end := digitsEnd(text, at, digits); end != noMatch {
			return end
		}
		if end := symbolsEnd(text, at, classSpace|classLetter|classNumber, false, lineBreaks); end != noMatch {
			return end
		}
		return spaceEnd(text, at)
	}
}

func casedWordsMatch(text string, at int) int {
	const prefixExcluded = classLineBreak | classLetter | classNumber
	if end := prefixed(text, at, prefixExcluded, smallWordEnd); end != noMatch {
		return end
	}
	if end := prefixed(text, at, prefixExcluded, capitalWordEnd); end != noMatch {
		return end
	}
	if end := digitsEnd(text, at, maxGroupedDigits); end != noMatch {
		return end
	}
	if end := symbolsEnd(text, at, classSpace|classLetter|classNumber, false, lineBreaksSlash); end != noMatch {
		return end
	}
	return spaceEnd(text, at)
}

func punctuationWordsMatch(text string, at int) int {
	if end := asciiPunctuationWordEnd(text, at); end != noMatch {
		return end
	}
	if end := prefixed(text, at, classLineBreak|classLetter|classPunctuation|classSymbol, letterMarkRunEnd); end != noMatch {
		return end
	}
	if end := symbolsEnd(text, at, classPunctuation|classSymbol, true, lineBreaks); end != noMatch {
		return end
	}
	return spaceEnd(text, at)
}

// contractionEnd is (?i:'s|'t|'re|'ve|'m|'ll|'d).
func contractionEnd(text string, at int) int {
	if at+1 >= len(text) || text[at] != '\'' {
		return noMatch
	}
	switch foldASCII(text[at+1]) {
	case 's', 't', 'm', 'd':
		return at + 2
	case 'r', 'v':
		if at+2 < len(text) && foldASCII(text[at+2]) == 'e' {
			return at + 3
		}
	case 'l':
		if at+2 < len(text) && foldASCII(text[at+2]) == 'l' {
			return at + 3
		}
	}
	return noMatch
}

func foldASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + 'a' - 'A'
	}
	return value
}

// prefixed is `[^excluded]?` followed by rest: the prefix is tried first and given back when rest fails after it.
func prefixed(text string, at int, excluded classFlags, rest matchFunc) int {
	_, flags, width := peek(text, at)
	if width == endOfText {
		return noMatch
	}
	if flags&excluded == 0 {
		if end := rest(text, at+width); end != noMatch {
			return end
		}
	}
	return rest(text, at)
}

// letterRunEnd is \p{L}+.
func letterRunEnd(text string, at int) int {
	return nonEmptyRunEnd(text, at, classLetter)
}

// letterMarkRunEnd is [\p{L}\p{M}]+.
func letterMarkRunEnd(text string, at int) int {
	return nonEmptyRunEnd(text, at, classLetter|classMark)
}

func nonEmptyRunEnd(text string, at int, mask classFlags) int {
	if end := runEnd(text, at, mask); end > at {
		return end
	}
	return noMatch
}

// smallWordEnd is [\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+ with an optional contraction: the capitals give back until a small letter follows.
func smallWordEnd(text string, at int) int {
	small := noMatch
	for position := at; ; {
		_, flags, width := peek(text, position)
		if width == endOfText {
			break
		}
		if flags&classSmall != 0 {
			small = position
		}
		if flags&classCapital == 0 {
			break
		}
		position += width
	}
	if small == noMatch {
		return noMatch
	}
	return optionalContractionEnd(text, runEnd(text, small, classSmall))
}

// capitalWordEnd is [\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]* with an optional contraction.
func capitalWordEnd(text string, at int) int {
	capitals := runEnd(text, at, classCapital)
	if capitals == at {
		return noMatch
	}
	return optionalContractionEnd(text, runEnd(text, capitals, classSmall))
}

func optionalContractionEnd(text string, at int) int {
	if end := contractionEnd(text, at); end != noMatch {
		return end
	}
	return at
}

// digitsEnd is \p{N}{1,limit}.
func digitsEnd(text string, at int, limit int) int {
	end := at
	for range limit {
		_, flags, width := peek(text, end)
		if width == endOfText || flags&classNumber == 0 {
			break
		}
		end += width
	}
	if end == at {
		return noMatch
	}
	return end
}

// symbolsEnd is ` ?[run]+[trailing]*`, where the run is the classes in mask, or every other character when present is false.
func symbolsEnd(text string, at int, mask classFlags, present bool, trailing string) int {
	if at >= len(text) {
		return noMatch
	}
	start := at
	if text[at] == ' ' {
		start++
	}
	var end int
	if present {
		end = runEnd(text, start, mask)
	} else {
		end = runEndAbsent(text, start, mask)
	}
	if end == start {
		return noMatch
	}
	for end < len(text) && strings.IndexByte(trailing, text[end]) >= 0 {
		end++
	}
	return end
}

// spaceEnd is `\s*[\r\n]+|\s+(?!\S)|\s+`: up to the last line break of the whitespace run, else the run less its last character when a non-space follows it.
func spaceEnd(text string, at int) int {
	lineBreakEnd, lastSpace := noMatch, noMatch
	position := at
	for {
		_, flags, width := peek(text, position)
		if width == endOfText || flags&classSpace == 0 {
			break
		}
		lastSpace = position
		if flags&classLineBreak != 0 {
			lineBreakEnd = position + width
		}
		position += width
	}
	switch {
	case lastSpace == noMatch:
		return noMatch
	case lineBreakEnd != noMatch:
		return lineBreakEnd
	case position >= len(text) || lastSpace == at:
		return position
	}
	return lastSpace
}

// asciiPunctuationWordEnd is an ASCII punctuation mark followed by [A-Za-z]+.
func asciiPunctuationWordEnd(text string, at int) int {
	if at+1 >= len(text) || text[at] >= utf8.RuneSelf || strings.IndexByte(asciiPunctuation, text[at]) < 0 {
		return noMatch
	}
	end := at + 1
	for end < len(text) && isASCIILetter(text[end]) {
		end++
	}
	if end == at+1 {
		return noMatch
	}
	return end
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

// cjkEnd is [一-龥぀-ゟ゠-ヿ]+.
func cjkEnd(text string, at int) int {
	end := at
	for {
		r, _, width := peek(text, end)
		if width == endOfText || !(r >= cjkFirstIdeograph && r <= cjkLastIdeograph || r >= hiraganaFirst && r <= hiraganaLast || r >= katakanaFirst && r <= katakanaLast) {
			break
		}
		end += width
	}
	if end == at {
		return noMatch
	}
	return end
}

// scanSplit appends the pieces of text that regexp2 would split it into, with the same keepMatches and keepGaps.
func (r splitRule) scanSplit(into []string, text string) []string {
	gapStart := 0
	for at := 0; at < len(text); {
		end := r.scan(text, at)
		if end == noMatch || end == at {
			_, width := utf8.DecodeRuneInString(text[at:])
			at += width
			continue
		}
		if r.keepGaps && at > gapStart {
			into = append(into, text[gapStart:at])
		}
		if r.keepMatches {
			into = append(into, text[at:end])
		}
		gapStart, at = end, end
	}
	if r.keepGaps && gapStart < len(text) {
		into = append(into, text[gapStart:])
	}
	return into
}

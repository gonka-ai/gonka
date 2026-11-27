package jsonshape

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	escapedRuneBytes   = len(`\u0000`)
	surrogatePairBytes = 2 * escapedRuneBytes
	shortEscapeBytes   = len(`\n`)
	hexBase            = 16
	hexLetterOffset    = 10
	highSurrogateFirst = 0xD800
	highSurrogateLast  = 0xDBFF
	lowSurrogateFirst  = 0xDC00
	lowSurrogateLast   = 0xDFFF
	invalidHexRune     = -1
	noValueEnd         = 0
)

// IsObject reports whether raw, past leading whitespace, opens an object.
func IsObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

// Object calls visit with each member's raw key and value until visit returns false.
func Object(raw []byte, visit func(key, value []byte) bool) {
	i := space(raw, 0)
	if i == len(raw) || raw[i] != '{' {
		return
	}
	for i = space(raw, i+1); i < len(raw) && raw[i] == '"'; i = space(raw, i+1) {
		end := skipString(raw, i+1)
		if end == notFound {
			return
		}
		colon := space(raw, end+1)
		if colon == len(raw) || raw[colon] != ':' {
			return
		}
		key := raw[i+1 : end]
		start := space(raw, colon+1)
		i = endValue(raw, start)
		if i <= start || !visit(key, raw[start:i]) {
			return
		}
		i = space(raw, i)
		if i == len(raw) || raw[i] != ',' {
			return
		}
	}
}

// Field returns the raw value of the last member named exactly name, as encoding/json reads it into a map.
func Field(raw []byte, name string) []byte {
	var result []byte
	Object(raw, func(key, value []byte) bool {
		if KeyEqual(key, name) {
			result = value
		}
		return true
	})
	return result
}

// FieldFold returns the raw value of the last member whose name matches name without regard to case, as encoding/json fills a struct field.
func FieldFold(raw []byte, name string) []byte {
	var result []byte
	Object(raw, func(key, value []byte) bool {
		if KeyEqualFold(key, name) {
			result = value
		}
		return true
	})
	return result
}

// KeyEqual reports whether the raw, possibly escaped, key spells name.
func KeyEqual(key []byte, name string) bool {
	return string(key) == name || equalKey(key, name, false)
}

// KeyEqualFold is KeyEqual without regard to case.
func KeyEqualFold(key []byte, name string) bool {
	if len(key) > len(name)*escapedRuneBytes {
		return false
	}
	return strings.EqualFold(string(key), name) || equalKey(key, name, true)
}

func equalKey(key []byte, name string, fold bool) bool {
	if len(key) > len(name)*escapedRuneBytes {
		return false
	}
	for len(key) > 0 && len(name) > 0 {
		r, size := utf8.DecodeRune(key)
		if r == '\\' {
			if len(key) < shortEscapeBytes {
				return false
			}
			size = shortEscapeBytes
			switch key[1] {
			case 'u':
				if len(key) < escapedRuneBytes {
					return false
				}
				r, size = hexRune(key[shortEscapeBytes:escapedRuneBytes]), escapedRuneBytes
				if r >= highSurrogateFirst && r <= highSurrogateLast && len(key) >= surrogatePairBytes && key[escapedRuneBytes] == '\\' && key[escapedRuneBytes+1] == 'u' {
					if second := hexRune(key[escapedRuneBytes+shortEscapeBytes : surrogatePairBytes]); second >= lowSurrogateFirst && second <= lowSurrogateLast {
						r, size = utf16.DecodeRune(r, second), surrogatePairBytes
					}
				}
				if utf16.IsSurrogate(r) {
					r = utf8.RuneError
				}
			case 'b':
				r = '\b'
			case 'f':
				r = '\f'
			case 'n':
				r = '\n'
			case 'r':
				r = '\r'
			case 't':
				r = '\t'
			default:
				r = rune(key[1])
			}
		}
		want, width := utf8.DecodeRuneInString(name)
		if r != want && (!fold || !foldRuneEqual(r, want)) {
			return false
		}
		key, name = key[size:], name[width:]
	}
	return len(key) == 0 && name == ""
}

func foldRuneEqual(a, b rune) bool {
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}

func hexRune(raw []byte) rune {
	var value rune
	for _, b := range raw {
		value *= hexBase
		switch {
		case '0' <= b && b <= '9':
			value += rune(b - '0')
		case 'a' <= b && b <= 'f':
			value += rune(b-'a') + hexLetterOffset
		case 'A' <= b && b <= 'F':
			value += rune(b-'A') + hexLetterOffset
		default:
			return invalidHexRune
		}
	}
	return value
}

func space(raw []byte, i int) int {
	for i < len(raw) {
		switch raw[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

func endValue(raw []byte, i int) int {
	if i == len(raw) {
		return i
	}
	if raw[i] == '"' {
		return skipString(raw, i+1) + 1
	}
	if raw[i] != '{' && raw[i] != '[' {
		for i < len(raw) && byteClass[raw[i]] == classPlain {
			i++
		}
		return i
	}
	depth := 1
	for i++; i < len(raw) && depth > 0; i++ {
		switch byteClass[raw[i]] {
		case classOpenArray, classOpenObject:
			depth++
		case classClose:
			depth--
		case classQuote:
			i = skipString(raw, i+1)
			if i == notFound {
				return noValueEnd
			}
		}
	}
	if depth != 0 {
		return noValueEnd
	}
	return i
}

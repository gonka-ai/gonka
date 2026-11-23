package jsonshape

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

func IsObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

func Object(raw []byte, visit func(key, value []byte) bool) {
	i := space(raw, 0)
	if i == len(raw) || raw[i] != '{' {
		return
	}
	for i = space(raw, i+1); i < len(raw) && raw[i] == '"'; i = space(raw, i+1) {
		end := skipString(raw, i+1)
		if end < 0 {
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

func Array(raw []byte, visit func(value []byte) bool) {
	i := space(raw, 0)
	if i == len(raw) || raw[i] != '[' {
		return
	}
	for i = space(raw, i+1); i < len(raw) && raw[i] != ']'; i = space(raw, i+1) {
		end := endValue(raw, i)
		if end <= i || !visit(raw[i:end]) {
			return
		}
		i = space(raw, end)
		if i == len(raw) || raw[i] != ',' {
			return
		}
	}
}

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

func KeyEqual(key []byte, name string) bool {
	return string(key) == name || equalKey(key, name, false)
}

func KeyEqualFold(key []byte, name string) bool {
	if len(key) > len(name)*6 {
		return false
	}
	return strings.EqualFold(string(key), name) || equalKey(key, name, true)
}

func equalKey(key []byte, name string, fold bool) bool {
	if len(key) > len(name)*6 {
		return false
	}
	for len(key) > 0 && len(name) > 0 {
		r, size := utf8.DecodeRune(key)
		if r == '\\' {
			if len(key) < 2 {
				return false
			}
			size = 2
			switch key[1] {
			case 'u':
				if len(key) < 6 {
					return false
				}
				r, size = hexRune(key[2:6]), 6
				if r >= 0xD800 && r <= 0xDBFF && len(key) >= 12 && key[6] == '\\' && key[7] == 'u' {
					if second := hexRune(key[8:12]); second >= 0xDC00 && second <= 0xDFFF {
						r, size = utf16.DecodeRune(r, second), 12
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
		value *= 16
		switch {
		case '0' <= b && b <= '9':
			value += rune(b - '0')
		case 'a' <= b && b <= 'f':
			value += rune(b-'a') + 10
		case 'A' <= b && b <= 'F':
			value += rune(b-'A') + 10
		default:
			return -1
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
			if i < 0 {
				return 0
			}
		}
	}
	if depth != 0 {
		return 0
	}
	return i
}

type Transform func(key []byte, depth int) (replacement string, change bool)

func Rewrite(raw []byte, transform Transform) ([]byte, bool, error) {
	if !json.Valid(raw) {
		return raw, false, ErrInvalid
	}
	if _, err := Check(raw, Limits{}); err != nil {
		return raw, false, err
	}
	dst, _, changed := rewrite(make([]byte, 0, len(raw)), raw, space(raw, 0), 1, transform)
	if !changed {
		return raw, false, nil
	}
	return dst, true, nil
}

func rewrite(dst, raw []byte, i, depth int, transform Transform) ([]byte, int, bool) {
	start := i
	switch raw[i] {
	case '{', '[':
		object := raw[i] == '{'
		dst = append(dst, raw[i])
		i = space(raw, i+1)
		wrote, changed := false, false
		for raw[i] != '}' && raw[i] != ']' {
			keyStart := i
			replacement, change := "", false
			if object {
				end := skipString(raw, i+1)
				replacement, change = transform(raw[i+1:end], depth)
				i = space(raw, space(raw, end+1)+1)
			}
			if !change || replacement != "" {
				if wrote {
					dst = append(dst, ',')
				}
				wrote = true
				if object {
					dst = append(dst, bytes.TrimSpace(raw[keyStart:i])...)
				}
			}
			if change {
				i = endValue(raw, i)
				dst = append(dst, replacement...)
				changed = true
			} else {
				var child bool
				dst, i, child = rewrite(dst, raw, i, depth+1, transform)
				changed = changed || child
			}
			i = space(raw, i)
			if raw[i] == ',' {
				i = space(raw, i+1)
			}
		}
		return append(dst, raw[i]), i + 1, changed
	default:
		i = endValue(raw, i)
		return append(dst, raw[start:i]...), i, false
	}
}

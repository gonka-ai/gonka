package main

import "bytes"

var (
	forcedStreamOptions = map[string]any{"include_usage": true}

	noResponseDataBody = []byte(`{"error":{"message":"no response data"}}`)

	nonFiniteLiterals = [][]byte{[]byte("-Infinity"), []byte("Infinity"), []byte("NaN")}
)

// replaceNonFiniteNumbers rewrites -Infinity/Infinity/NaN to null outside string literals: none is
// valid JSON, so a chunk carrying one would otherwise be dropped whole.
func replaceNonFiniteNumbers(body []byte) ([]byte, bool) {
	carries := false
	for _, literal := range nonFiniteLiterals {
		if bytes.Contains(body, literal) {
			carries = true
			break
		}
	}
	if !carries {
		return nil, false
	}
	out := make([]byte, 0, len(body))
	inString, escaped, replaced := false, false, false
	for index := 0; index < len(body); {
		current := body[index]
		switch {
		case escaped:
			escaped = false
		case inString && current == '\\':
			escaped = true
		case current == '"':
			inString = !inString
		case !inString:
			if literal := matchNonFinite(body[index:]); literal > 0 {
				out = append(out, []byte("null")...)
				index += literal
				replaced = true
				continue
			}
		}
		out = append(out, current)
		index++
	}
	return out, replaced
}

func matchNonFinite(tail []byte) int {
	for _, literal := range nonFiniteLiterals {
		if bytes.HasPrefix(tail, literal) {
			return len(literal)
		}
	}
	return 0
}

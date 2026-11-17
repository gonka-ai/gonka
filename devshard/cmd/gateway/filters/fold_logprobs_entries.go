package filters

import (
	"bytes"
	stdjson "encoding/json"
	"unicode/utf8"
)

var (
	lineSeparator      = []byte("\u2028")
	paragraphSeparator = []byte("\u2029")
)

func canonicalEntries(array []byte, intent LogprobIntent) ([]any, bool) {
	if len(array) == 0 || array[0] != '[' || !stdjson.Valid(array) || !canonicalJSON(array) {
		return nil, false
	}
	entries := []any{}
	for position := 1; array[position] != ']'; position++ {
		end := skipValidatedValue(array, position)
		entry := array[position:end]
		stripped, changed, handled := stripValidatedMembers(entry, intent)
		if !handled {
			return nil, false
		}
		if changed {
			entry = stripped
		}
		entries = append(entries, stdjson.RawMessage(bytes.Clone(entry)))
		position = end
		if array[position] == ']' {
			break
		}
	}
	return entries, true
}

func canonicalJSON(input []byte) bool {
	end, canonical := canonicalValue(input, 0)
	return canonical && end == len(input)
}

func canonicalValue(input []byte, position int) (int, bool) {
	if position >= len(input) {
		return 0, false
	}
	switch input[position] {
	case '{':
		return canonicalObject(input, position+1)
	case '[':
		return canonicalArray(input, position+1)
	case '"':
		return canonicalString(input, position)
	}
	end := skipValidatedValue(input, position)
	return end, end > position
}

func canonicalObject(input []byte, position int) (int, bool) {
	var previous []byte
	for first := true; position < len(input); first = false {
		if input[position] == '}' {
			return position + 1, true
		}
		if !first {
			if input[position] != ',' {
				return 0, false
			}
			position++
		}
		keyEnd, canonical := canonicalString(input, position)
		if !canonical {
			return 0, false
		}
		name := input[position+1 : keyEnd-1]
		if previous != nil && bytes.Compare(previous, name) >= 0 {
			return 0, false
		}
		previous = name
		if keyEnd >= len(input) || input[keyEnd] != ':' {
			return 0, false
		}
		if position, canonical = canonicalValue(input, keyEnd+1); !canonical {
			return 0, false
		}
	}
	return 0, false
}

func canonicalArray(input []byte, position int) (int, bool) {
	for first := true; position < len(input); first = false {
		if input[position] == ']' {
			return position + 1, true
		}
		if !first {
			if input[position] != ',' {
				return 0, false
			}
			position++
		}
		end, canonical := canonicalValue(input, position)
		if !canonical {
			return 0, false
		}
		position = end
	}
	return 0, false
}

func canonicalString(input []byte, position int) (int, bool) {
	if position >= len(input) || input[position] != '"' {
		return 0, false
	}
	for position++; position < len(input); {
		symbol := input[position]
		switch {
		case symbol == '"':
			return position + 1, true
		case symbol == '\\' || symbol < firstNonControlByte:
			return 0, false
		case symbol < utf8.RuneSelf:
			position++
			continue
		case bytes.HasPrefix(input[position:], lineSeparator) || bytes.HasPrefix(input[position:], paragraphSeparator):
			return 0, false
		}
		decoded, size := utf8.DecodeRune(input[position:])
		if decoded == utf8.RuneError && size <= 1 {
			return 0, false
		}
		position += size
	}
	return 0, false
}

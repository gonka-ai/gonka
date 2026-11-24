package filters

import (
	"bytes"
	stdjson "encoding/json"
)

type memberStripper struct {
	input              []byte
	output             []byte
	fields             map[string]struct{}
	emptiesTopLogprobs bool
	changed            bool
}

const (
	topLogprobsKey     = "top_logprobs"
	linearKeyScanLimit = 16
)

var emptyJSONArray = []byte("[]")

// stripMembersWithoutDecoding applies stripDecodedFields to the payload's bytes. See README.md, "Stripping without decoding".
func stripMembersWithoutDecoding(payload []byte, intent LogprobIntent) (stripped []byte, changed, handled bool) {
	if !stdjson.Valid(payload) {
		return nil, false, false
	}
	return stripValidatedMembers(payload, intent)
}

func stripValidatedMembers(payload []byte, intent LogprobIntent) (stripped []byte, changed, handled bool) {
	stripper := memberStripper{
		input:              payload,
		output:             make([]byte, 0, len(payload)),
		fields:             intent.strippedFields(),
		emptiesTopLogprobs: intent.Keep && !intent.KeepTop,
	}
	end, readable := stripper.value(skipWhitespace(payload, 0))
	if !readable || skipWhitespace(payload, end) != len(payload) {
		return nil, false, false
	}
	return stripper.output, stripper.changed, true
}

func (s *memberStripper) value(position int) (int, bool) {
	switch s.input[position] {
	case '{':
		return s.object(position)
	case '[':
		return s.array(position)
	}
	end := skipValidatedValue(s.input, position)
	s.output = append(s.output, s.input[position:end]...)
	return end, true
}

func (s *memberStripper) object(position int) (int, bool) {
	s.output = append(s.output, '{')
	position = skipWhitespace(s.input, position+1)
	var names seenKeys
	written := 0
	for s.input[position] != '}' {
		keyEnd := stringEnd(s.input, position)
		name := s.input[position+1 : keyEnd-1]
		if bytes.IndexByte(name, '\\') >= 0 || !names.add(name) {
			return 0, false
		}
		valueStart := skipWhitespace(s.input, skipWhitespace(s.input, keyEnd)+1)
		valueEnd := 0
		if _, stripped := s.fields[string(name)]; stripped {
			valueEnd = skipValidatedValue(s.input, valueStart)
			s.changed = true
		} else {
			if written > 0 {
				s.output = append(s.output, ',')
			}
			s.output = append(s.output, s.input[position:keyEnd]...)
			s.output = append(s.output, ':')
			if s.emptiesTopLogprobs && string(name) == topLogprobsKey {
				valueEnd = skipValidatedValue(s.input, valueStart)
				s.changed = s.changed || !isEmptyArray(s.input[valueStart:valueEnd])
				s.output = append(s.output, emptyJSONArray...)
			} else {
				end, readable := s.value(valueStart)
				if !readable {
					return 0, false
				}
				valueEnd = end
			}
			written++
		}
		position = skipSeparator(s.input, valueEnd)
	}
	s.output = append(s.output, '}')
	return position + 1, true
}

func (s *memberStripper) array(position int) (int, bool) {
	s.output = append(s.output, '[')
	position = skipWhitespace(s.input, position+1)
	for written := 0; s.input[position] != ']'; written++ {
		if written > 0 {
			s.output = append(s.output, ',')
		}
		end, readable := s.value(position)
		if !readable {
			return 0, false
		}
		position = skipSeparator(s.input, end)
	}
	s.output = append(s.output, ']')
	return position + 1, true
}

type seenKeys struct {
	scanned [][]byte
	indexed map[string]struct{}
}

func (keys *seenKeys) add(name []byte) bool {
	if keys.indexed != nil {
		if _, seen := keys.indexed[string(name)]; seen {
			return false
		}
		keys.indexed[string(name)] = struct{}{}
		return true
	}
	for _, seen := range keys.scanned {
		if bytes.Equal(seen, name) {
			return false
		}
	}
	keys.scanned = append(keys.scanned, name)
	if len(keys.scanned) > linearKeyScanLimit {
		keys.indexed = make(map[string]struct{}, len(keys.scanned))
		for _, scanned := range keys.scanned {
			keys.indexed[string(scanned)] = struct{}{}
		}
		keys.scanned = nil
	}
	return true
}

func isEmptyArray(value []byte) bool {
	return len(value) >= len(emptyJSONArray) && value[0] == '[' && skipWhitespace(value, 1) == len(value)-1
}

func skipSeparator(input []byte, position int) int {
	position = skipWhitespace(input, position)
	if input[position] == ',' {
		position = skipWhitespace(input, position+1)
	}
	return position
}

func stringEnd(input []byte, opening int) int {
	return opening + 1 + stringLiteralEnd(input[opening+1:])
}

func skipValidatedValue(input []byte, position int) int {
	switch input[position] {
	case '"':
		return stringEnd(input, position)
	case '{', '[':
		depth := 0
		for position < len(input) {
			switch input[position] {
			case '"':
				position = stringEnd(input, position)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			position++
			if depth == 0 {
				return position
			}
		}
		return position
	}
	for position < len(input) && !endsScalar(input[position]) {
		position++
	}
	return position
}

func endsScalar(symbol byte) bool {
	switch symbol {
	case ',', ']', '}':
		return true
	}
	return isJSONWhitespace(symbol)
}

func skipWhitespace(input []byte, position int) int {
	for position < len(input) && isJSONWhitespace(input[position]) {
		position++
	}
	return position
}

func isJSONWhitespace(symbol byte) bool {
	switch symbol {
	case ' ', '\t', '\r', '\n':
		return true
	}
	return false
}

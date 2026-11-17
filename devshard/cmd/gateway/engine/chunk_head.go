package engine

import (
	"strings"

	"devshard/cmd/gateway/filters"
)

// maxEmptyChunkLogged is how much of a contentless chunk the log keeps. See race.md, "Reading an empty answer back".
const maxEmptyChunkLogged = 512

// elidedValue stands in for an array the log drops.
const elidedValue = "[...]"

// bulkyFields name the values the gateway asks a host for and strips again.
var bulkyFields = [][]byte{
	[]byte(`"prompt_token_ids":`),
	[]byte(`"token_ids":`),
	[]byte(`"prompt_logprobs":`),
	[]byte(`"logprobs":`),
}

// keepChunkHeads keeps the start of the first and the last chunk that carried nothing.
func (s *attemptState) keepChunkHeads(chunk []byte) {
	head := chunkHead(chunk)
	if head == "" {
		return
	}
	if s.firstChunkHead == "" {
		s.firstChunkHead = head
	}
	s.lastChunkHead = head
}

// chunkHead renders a chunk's start with the bulky values elided, terminator aside.
func chunkHead(chunk []byte) string {
	chunk = filters.TrimSSEDone(chunk)
	if len(chunk) == 0 {
		return ""
	}
	return strings.ToValidUTF8(string(elideBulkyValues(chunk, maxEmptyChunkLogged)), "")
}

// elideBulkyValues copies a chunk up to budget bytes, writing a marker in place of each bulky value.
func elideBulkyValues(chunk []byte, budget int) []byte {
	head := make([]byte, 0, budget+len(elidedValue))
	for index := 0; index < len(chunk) && len(head) < budget; {
		field, elided := bulkyValueAt(chunk, index)
		if !elided {
			head = append(head, chunk[index])
			index++
			continue
		}
		head = append(head, field...)
		head = append(head, elidedValue...)
		index = pastValue(chunk, index+len(field))
	}
	return head
}

// bulkyValueAt reports the bulky field name starting here, and only when its value is one the log can elide.
func bulkyValueAt(chunk []byte, index int) ([]byte, bool) {
	for _, field := range bulkyFields {
		if index+len(field) > len(chunk) || string(chunk[index:index+len(field)]) != string(field) {
			continue
		}
		if opening := firstNonSpace(chunk, index+len(field)); opening == '[' || opening == '{' {
			return field, true
		}
	}
	return nil, false
}

func firstNonSpace(chunk []byte, index int) byte {
	for ; index < len(chunk); index++ {
		if chunk[index] != ' ' && chunk[index] != '\t' {
			return chunk[index]
		}
	}
	return 0
}

// pastValue returns the index just past one bracketed JSON value, counting only its own bracket pair.
func pastValue(chunk []byte, index int) int {
	for index < len(chunk) && (chunk[index] == ' ' || chunk[index] == '\t') {
		index++
	}
	if index >= len(chunk) {
		return index
	}
	opening := chunk[index]
	closing := byte(']')
	if opening == '{' {
		closing = '}'
	}
	depth, inString, escaped := 0, false, false
	for ; index < len(chunk); index++ {
		character := chunk[index]
		switch {
		case escaped:
			escaped = false
		case inString && character == '\\':
			escaped = true
		case character == '"':
			inString = !inString
		case inString:
		case character == opening:
			depth++
		case character == closing:
			if depth--; depth == 0 {
				return index + 1
			}
		}
	}
	return index
}

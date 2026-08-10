package completionapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Per-chunk inference-id rewrite for streamed SSE lines.
//
// The historical path (map round-trip via encoding/json) alphabetizes keys and
// allocates heavily on logprobs/token-id chunks. The surgical path here splices
// only the depth-1 "id" value, preserving upstream formatting. idPatcher
// memoizes the exact `"id":"…"` span for the rest of a stream so the common
// case is a verified byte patch with no parse and no allocations.
//
// Nested keys (e.g. choices[].delta.tool_calls[].id) and id-like text inside
// content are left untouched. See Step 5b in
// devshard/docs/gateway-attempt-reconnect-plan.md.

var (
	dataPrefixBytes  = []byte(DataPrefix)
	doneMarkerBytes  = []byte("[DONE]")
	errNotJSONObject = errors.New("chunk body is not a JSON object")
	errIDNotFound    = errors.New("no depth-1 \"id\" key")
)

// findTopLevelIDSpan returns the byte span of the depth-1 `"id":"…"` pair,
// from the opening quote of the key through the closing quote of the value.
// Nested "id" keys (e.g. choices[].delta.tool_calls[].id) are skipped.
func findTopLevelIDSpan(body []byte) (int, int, error) {
	i := skipJSONSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, errNotJSONObject
	}
	i++
	for {
		i = skipJSONSpace(body, i)
		if i >= len(body) || body[i] == '}' {
			return 0, 0, errIDNotFound
		}
		if body[i] != '"' {
			return 0, 0, fmt.Errorf("expected object key at %d", i)
		}
		keyStart := i
		keyEnd, err := skipJSONString(body, i)
		if err != nil {
			return 0, 0, err
		}
		key := body[keyStart+1 : keyEnd-1]

		i = skipJSONSpace(body, keyEnd)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, fmt.Errorf("expected ':' at %d", i)
		}
		i = skipJSONSpace(body, i+1)
		valStart := i
		valEnd, err := skipJSONValue(body, i)
		if err != nil {
			return 0, 0, err
		}
		if string(key) == "id" {
			if body[valStart] != '"' {
				return 0, 0, fmt.Errorf("depth-1 id is not a string")
			}
			return keyStart, valEnd, nil
		}

		i = skipJSONSpace(body, valEnd)
		if i >= len(body) {
			return 0, 0, errIDNotFound
		}
		switch body[i] {
		case ',':
			i++
		case '}':
			return 0, 0, errIDNotFound
		default:
			return 0, 0, fmt.Errorf("unexpected byte %q at %d", body[i], i)
		}
	}
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// skipJSONString expects b[i] == '"' and returns the index just past the
// closing quote.
func skipJSONString(b []byte, i int) (int, error) {
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
			continue
		case '"':
			return i + 1, nil
		}
		i++
	}
	return 0, errors.New("unterminated JSON string")
}

// skipJSONValue returns the index just past the value starting at i.
func skipJSONValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, errors.New("unexpected end of value")
	}
	switch b[i] {
	case '"':
		return skipJSONString(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				next, err := skipJSONString(b, i)
				if err != nil {
					return 0, err
				}
				i = next
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, nil
				}
			}
			i++
		}
		return 0, errors.New("unterminated JSON object/array")
	default:
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return i, nil
			}
			i++
		}
		return i, nil
	}
}

// idRewriteSplice appends the rewritten line to dst, preserving upstream key
// order and formatting outside the id span. Inserts the key when absent.
func idRewriteSplice(dst, line []byte, id string) ([]byte, error) {
	if !bytes.HasPrefix(line, dataPrefixBytes) {
		return append(dst, line...), nil
	}
	body := bytes.TrimSpace(line[len(dataPrefixBytes):])
	if bytes.HasPrefix(body, doneMarkerBytes) {
		return append(dst, line...), nil
	}

	keyStart, valEnd, err := findTopLevelIDSpan(body)
	if errors.Is(err, errIDNotFound) {
		return insertTopLevelID(dst, body, id)
	}
	if err != nil {
		return dst, err
	}
	dst = append(dst, dataPrefixBytes...)
	dst = append(dst, body[:keyStart]...)
	dst = appendIDPair(dst, id)
	dst = append(dst, body[valEnd:]...)
	return dst, nil
}

func insertTopLevelID(dst, body []byte, id string) ([]byte, error) {
	open := skipJSONSpace(body, 0)
	if open >= len(body) || body[open] != '{' {
		return dst, errNotJSONObject
	}
	rest := body[open+1:]
	dst = append(dst, dataPrefixBytes...)
	dst = append(dst, body[:open+1]...)
	dst = appendIDPair(dst, id)
	if skipJSONSpace(rest, 0) < len(rest) && rest[skipJSONSpace(rest, 0)] != '}' {
		dst = append(dst, ',')
	}
	dst = append(dst, rest...)
	return dst, nil
}

func appendIDPair(dst []byte, id string) []byte {
	if jsonStringNeedsEscape(id) {
		dst = append(dst, `"id":`...)
		quoted, err := json.Marshal(id)
		if err != nil {
			// json.Marshal on a string only fails on invalid UTF-8; replace.
			quoted = []byte(`""`)
		}
		return append(dst, quoted...)
	}
	dst = append(dst, `"id":"`...)
	dst = append(dst, id...)
	return append(dst, '"')
}

func jsonStringNeedsEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' {
			return true
		}
	}
	return false
}

// idPatcher exploits the fact that every chunk of one SSE stream carries the
// same upstream id at the same offset. It learns the exact `"id":"…"` bytes once
// via the depth-aware scan, then verifies that literal at the learned offset —
// O(len(pattern)), no scan, no parse, and no possibility of matching an id
// nested inside choices. Any deviation falls back to a re-learn.
type idPatcher struct {
	id     string
	search []byte
	repl   []byte
	hint   int
	hits   int
	learns int
}

func (p *idPatcher) rewrite(dst, line []byte) ([]byte, error) {
	if !bytes.HasPrefix(line, dataPrefixBytes) {
		return append(dst, line...), nil
	}
	body := bytes.TrimSpace(line[len(dataPrefixBytes):])
	if bytes.HasPrefix(body, doneMarkerBytes) {
		return append(dst, line...), nil
	}

	if p.search != nil && p.hint+len(p.search) <= len(body) &&
		bytes.Equal(body[p.hint:p.hint+len(p.search)], p.search) {
		p.hits++
		dst = append(dst, dataPrefixBytes...)
		dst = append(dst, body[:p.hint]...)
		dst = append(dst, p.repl...)
		dst = append(dst, body[p.hint+len(p.search):]...)
		return dst, nil
	}

	keyStart, valEnd, err := findTopLevelIDSpan(body)
	if errors.Is(err, errIDNotFound) {
		return insertTopLevelID(dst, body, p.id)
	}
	if err != nil {
		return dst, err
	}
	p.learns++
	p.hint = keyStart
	p.search = append(p.search[:0], body[keyStart:valEnd]...)
	p.repl = appendIDPair(p.repl[:0], p.id)

	dst = append(dst, dataPrefixBytes...)
	dst = append(dst, body[:keyStart]...)
	dst = append(dst, p.repl...)
	dst = append(dst, body[valEnd:]...)
	return dst, nil
}

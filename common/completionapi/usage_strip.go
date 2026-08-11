package completionapi

import (
	"bytes"
)

// stripTopLevelNullUsageFromSSELine removes a depth-1 `"usage":null` pair from
// an SSE data line. vLLM with include_usage emits that noise on every content
// chunk; stripping it keeps the host→gateway wire and stored events smaller
// without a full JSON round-trip. Real usage objects are left untouched.
// Nested "usage" keys are never modified.
//
// When unchanged, returns the original line and false. When stripped, returns a
// newly allocated line and true.
func stripTopLevelNullUsageFromSSELine(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, dataPrefixBytes) {
		return line, false
	}
	body := bytes.TrimSpace(line[len(dataPrefixBytes):])
	if bytes.HasPrefix(body, doneMarkerBytes) {
		return line, false
	}
	newBody, changed := removeTopLevelNullUsage(body)
	if !changed {
		return line, false
	}
	out := make([]byte, 0, len(dataPrefixBytes)+len(newBody))
	out = append(out, dataPrefixBytes...)
	out = append(out, newBody...)
	return out, true
}

// RemoveTopLevelUsage removes the depth-1 "usage" pair from a chat completion
// (or chunk) JSON object whatever its value, and is how the gateway keeps a
// forced include_usage payload away from a client that never asked for usage —
// including the case where a host puts usage on the same chunk as its choices.
// Nested "usage" keys are never modified.
//
// When unchanged, returns the original body and false. When stripped, returns a
// newly allocated body and true.
func RemoveTopLevelUsage(body []byte) ([]byte, bool) {
	return removeTopLevelUsage(body, false)
}

// TopLevelJSONValue returns the raw depth-1 value for key without parsing the
// rest of the document. The returned slice aliases body. Reports false when
// body is not a JSON object or carries no such key at depth 1.
func TopLevelJSONValue(body []byte, key string) ([]byte, bool) {
	_, valStart, valEnd, err := findTopLevelKeySpan(body, key)
	if err != nil {
		return nil, false
	}
	return body[valStart:valEnd], true
}

func removeTopLevelNullUsage(body []byte) ([]byte, bool) {
	return removeTopLevelUsage(body, true)
}

func removeTopLevelUsage(body []byte, onlyNull bool) ([]byte, bool) {
	keyStart, valStart, valEnd, err := findTopLevelKeySpan(body, "usage")
	if err != nil {
		return body, false
	}
	if onlyNull && !bytes.Equal(body[valStart:valEnd], []byte("null")) {
		return body, false
	}

	// Prefer dropping a trailing comma after the value so a leading key stays tidy:
	// {"usage":…,"choices":[]} → {"choices":[]}
	after := skipJSONSpace(body, valEnd)
	if after < len(body) && body[after] == ',' {
		out := make([]byte, 0, len(body)-(after+1-keyStart))
		out = append(out, body[:keyStart]...)
		out = append(out, body[after+1:]...)
		return out, true
	}

	// Otherwise drop a preceding comma:
	// {"id":"x","usage":…} → {"id":"x"}
	before := keyStart
	for before > 0 {
		switch body[before-1] {
		case ' ', '\t', '\r', '\n':
			before--
		default:
			goto checkPrevComma
		}
	}
checkPrevComma:
	if before > 0 && body[before-1] == ',' {
		comma := before - 1
		out := make([]byte, 0, len(body)-(valEnd-comma))
		out = append(out, body[:comma]...)
		out = append(out, body[valEnd:]...)
		return out, true
	}

	// Sole key (or first key without a following comma).
	out := make([]byte, 0, len(body)-(valEnd-keyStart))
	out = append(out, body[:keyStart]...)
	out = append(out, body[valEnd:]...)
	return out, true
}

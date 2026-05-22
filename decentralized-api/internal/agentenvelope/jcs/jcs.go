// Package jcs implements the RFC 8785 JSON Canonicalization Scheme.
//
// JCS produces a deterministic, byte-stable serialization of a JSON value so
// that a signature produced over one serialization verifies against an
// independently produced serialization of the same logical value. The agent
// envelope relies on this for cross-implementation signature verification: a
// passport canonicalized by an APS client SDK must produce the same bytes
// when re-canonicalized here in Go before the principal signature is checked.
//
// Gonka's existing utils.CanonicalizeJSON is not RFC 8785 strict. It re-encodes
// through encoding/json, which appends a trailing newline, re-formats numbers
// through float64, and escapes U+2028 and U+2029. This package vendors a
// strict implementation rather than depending on it.
//
// Number handling: the agent envelope schema contains no JSON numbers (every
// field is a string, an object, or an array of strings). Numbers are still
// canonicalized for completeness. Integer values within the IEEE-754 double
// safe-integer range are emitted as plain decimals. Non-integer values are
// emitted with Go shortest round-trip formatting, which matches the ECMAScript
// algorithm for common cases. Callers that canonicalize arbitrary non-integer
// numbers should verify the result against RFC 8785 Appendix B.
package jcs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
)

// Canonicalize parses input as a single JSON value and returns its RFC 8785
// canonical serialization. It returns an error if input is not valid JSON or
// contains trailing content after the first value.
func Canonicalize(input []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()

	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jcs: parse input: %w", err)
	}

	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("jcs: unexpected trailing content after JSON value")
	}

	var buf bytes.Buffer
	if err := write(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func write(buf *bytes.Buffer, v interface{}) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeString(buf, t)
	case json.Number:
		s, err := canonicalNumber(string(t))
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case []interface{}:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortKeys(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			if err := write(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported JSON type %T", v)
	}
	return nil
}

// sortKeys orders object member names by their UTF-16 code units, per
// RFC 8785 section 3.2.3.
func sortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		return lessUTF16(keys[i], keys[j])
	})
}

func lessUTF16(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

const hexDigits = "0123456789abcdef"

// writeString emits s as a JSON string using RFC 8785 escaping: the seven
// short escapes, \u00XX with lowercase hex for the remaining C0 control
// characters, and literal UTF-8 for everything else. U+2028 and U+2029 are
// emitted literally, matching ECMAScript JSON.stringify.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hexDigits[(r>>4)&0xf])
				buf.WriteByte(hexDigits[r&0xf])
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// canonicalNumber returns the RFC 8785 canonical serialization of a JSON
// number given in its original textual form.
func canonicalNumber(raw string) (string, error) {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", fmt.Errorf("jcs: invalid number %q: %w", raw, err)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("jcs: non-finite number %q", raw)
	}
	if f == math.Trunc(f) && math.Abs(f) < (1<<53) {
		// Integer in the safe-integer range. Normalizes -0 to 0.
		return strconv.FormatInt(int64(f), 10), nil
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}

package devshard

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CanonicalizeJSON returns a deterministic JSON encoding with sorted keys and
// no HTML escaping. Used to ensure hash consistency across components.
func CanonicalizeJSON(data []byte) ([]byte, error) {
	if shape := ScanJSONShape(data); shape.MaxDepth > MaxCanonicalJSONDepth || shape.Composites > MaxCanonicalJSONComposites {
		return nil, fmt.Errorf("canonicalize json: %w: depth %d, %d objects and arrays", ErrJSONTooComplex, shape.MaxDepth, shape.Composites)
	}
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("canonicalize json: %w", err)
	}

	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "")
	enc.SetEscapeHTML(false)

	if err := encodeCanonical(enc, obj); err != nil {
		return nil, err
	}

	// json.Encoder appends a newline; trim it for hash consistency.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

const (
	MaxCanonicalJSONDepth      = 64
	MaxCanonicalJSONComposites = 1 << 16
)

var ErrJSONTooComplex = errors.New("json too complex")

type JSONShape struct {
	MaxDepth      int
	Composites    int
	TopLevelItems int
}

func ScanJSONShape(data []byte) JSONShape {
	var s JSONShape
	depth := 0
	inString, escaped := false, false
	seen, topArray, expect := false, false, false
	for _, c := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if !seen {
			seen = true
			topArray = c == '['
		}
		if topArray && depth == 1 {
			switch {
			case c == ',':
				expect = true
			case c != ']' && expect:
				s.TopLevelItems++
				expect = false
			}
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			s.Composites++
			if depth > s.MaxDepth {
				s.MaxDepth = depth
			}
			if depth == 1 && topArray {
				expect = true
			}
		case '}', ']':
			depth--
		}
	}
	return s
}

// CanonicalPromptHash returns sha256(canonicalize(prompt)).
// All components (user SDK, host, engine, validator) must use this
// to compute prompt hashes for consistency.
func CanonicalPromptHash(prompt []byte) ([]byte, error) {
	canonical, err := CanonicalizeJSON(prompt)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(canonical)
	return h[:], nil
}

// JSONNumericFloat64 converts a JSON-decoded numeric value to float64. Accepts
// float64, int / int64 / uint64, json.Number, and string (trimmed and parsed)
// because some clients serialize numerics as strings for fields like
// temperature / top_p.
func JSONNumericFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

// JSONNumericUint64 converts a JSON-decoded numeric value to uint64. Accepts
// float64 (only when integer-valued and non-negative), int / int64 / uint64,
// and json.Number. Returns (0, false) for any other type or out-of-range value.
func JSONNumericUint64(value any) (uint64, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 || v != float64(uint64(v)) {
			return 0, false
		}
		return uint64(v), true
	case uint64:
		return v, true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 0 {
			return 0, false
		}
		return uint64(n), true
	default:
		return 0, false
	}
}

func encodeCanonical(enc *json.Encoder, v interface{}) error {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		sorted := make(map[string]interface{}, len(val))
		for _, k := range keys {
			sorted[k] = val[k]
		}
		return enc.Encode(sorted)
	default:
		return enc.Encode(val)
	}
}

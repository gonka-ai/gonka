package devshard

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalizeJSON returns a deterministic JSON encoding with sorted keys and
// no HTML escaping. Used to ensure hash consistency across components.
func CanonicalizeJSON(data []byte) ([]byte, error) {
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

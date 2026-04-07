package subnet

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

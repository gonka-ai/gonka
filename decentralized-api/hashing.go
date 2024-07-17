package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"sort"
)

// CanonicalizeJSON takes a JSON byte slice and returns a canonicalized JSON string
func CanonicalizeJSON(jsonBytes []byte) (string, error) {
	var jsonObj interface{}
	// Decode JSON into a generic interface
	if err := json.Unmarshal(jsonBytes, &jsonObj); err != nil {
		return "", err
	}
	// add a random seed if it doesn't exist in jsonObj
	if _, ok := jsonObj.(map[string]interface{})["seed"]; !ok {
		jsonObj.(map[string]interface{})["seed"] = rand.Int31()
	}

	// Use a buffer to write the canonical JSON
	buf := &bytes.Buffer{}
	encoder := json.NewEncoder(buf)
	encoder.SetIndent("", "")
	encoder.SetEscapeHTML(false)

	// Recursively sort keys and encode
	if err := encodeCanonical(encoder, jsonObj); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// encodeCanonical encodes JSON ensuring that keys are sorted
func encodeCanonical(encoder *json.Encoder, jsonObj interface{}) error {
	switch v := jsonObj.(type) {
	case map[string]interface{}:
		// Sort keys
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Create a sorted map
		sortedMap := make(map[string]interface{}, len(v))
		for _, k := range keys {
			sortedMap[k] = v[k]
		}

		// Encode sorted map
		if err := encoder.Encode(sortedMap); err != nil {
			return err
		}

	case []interface{}:
		// Encode array elements
		if err := encoder.Encode(v); err != nil {
			return err
		}

	default:
		// Encode other types
		if err := encoder.Encode(v); err != nil {
			return err
		}
	}

	return nil
}

func generateSHA256Hash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

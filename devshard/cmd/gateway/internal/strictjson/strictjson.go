// Package strictjson decodes bytes a host or a client sent. See ../README.md.
package strictjson

import (
	"bytes"
	stdjson "encoding/json"
	"errors"

	json "github.com/goccy/go-json"
)

// ErrInvalid is what Unmarshal answers for bytes that are not one valid JSON value. See ../README.md.
var ErrInvalid = errors.New("invalid JSON")

// Unmarshal hands goccy only what the standard library accepts as valid JSON. See ../README.md.
func Unmarshal(data []byte, target any) error {
	if !stdjson.Valid(data) {
		return ErrInvalid
	}
	return json.Unmarshal(data, target)
}

// DecodeKnownFields decodes exactly one JSON value into target and refuses any key target does not declare.
func DecodeKnownFields(data []byte, target any) error {
	if !stdjson.Valid(data) {
		return ErrInvalid
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

package paramvalidators

import (
	"errors"
	"fmt"
)

// MetadataValidator enforces the OpenAI Chat Completions `metadata` contract: an optional
// object with at most 16 string-keyed entries, keys up to 64 characters, values that are
// strings up to 512 characters. The field has no inference-side meaning for vLLM (it is
// ignored upstream) — clients use it for distributed tracing (LangSmith, OpenAI tracing,
// W3C trace-context propagation) and A/B test tagging. Validating at the gateway boundary
// keeps the same bounds the OpenAI API itself enforces, so the field is bounded surface
// rather than an unbounded passthrough.
//
// Zero-valued limit fields fall back to the OpenAI-documented defaults.
type MetadataValidator struct {
	MaxKeys     int
	MaxKeyLen   int
	MaxValueLen int
}

// ErrMetadataShape covers the wrapper-level rejection: metadata must be a JSON object.
// ErrMetadataKeyCount, ErrMetadataKey, ErrMetadataValue mark the OpenAI-spec bound
// violations.
var (
	ErrMetadataShape    = errors.New("metadata: invalid wrapper shape")
	ErrMetadataKeyCount = errors.New("metadata: key count exceeded")
	ErrMetadataKey      = errors.New("metadata: key invalid")
	ErrMetadataValue    = errors.New("metadata: value invalid")
)

// OpenAI's documented bounds — see https://platform.openai.com/docs/api-reference/chat
const (
	defaultMetadataMaxKeys     = 16
	defaultMetadataMaxKeyLen   = 64
	defaultMetadataMaxValueLen = 512
)

func (v MetadataValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["metadata"]
	if !exists {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrMetadataShape)
	}
	maxKeys := v.MaxKeys
	if maxKeys == 0 {
		maxKeys = defaultMetadataMaxKeys
	}
	maxKeyLen := v.MaxKeyLen
	if maxKeyLen == 0 {
		maxKeyLen = defaultMetadataMaxKeyLen
	}
	maxValueLen := v.MaxValueLen
	if maxValueLen == 0 {
		maxValueLen = defaultMetadataMaxValueLen
	}
	if len(obj) > maxKeys {
		return fmt.Errorf("%w: %d > %d", ErrMetadataKeyCount, len(obj), maxKeys)
	}
	for key, val := range obj {
		if len(key) > maxKeyLen {
			return fmt.Errorf("%w: key length %d > %d", ErrMetadataKey, len(key), maxKeyLen)
		}
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("%w: value for %q must be a string", ErrMetadataValue, key)
		}
		if len(s) > maxValueLen {
			return fmt.Errorf("%w: value for %q length %d > %d", ErrMetadataValue, key, len(s), maxValueLen)
		}
	}
	return nil
}

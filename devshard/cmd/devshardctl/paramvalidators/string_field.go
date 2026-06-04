package paramvalidators

import (
	"errors"
	"fmt"
)

var (
	ErrStringFieldShape  = errors.New("invalid wrapper shape")
	ErrStringFieldLength = errors.New("length exceeded")
)

// StringFieldValidator enforces a string type and a byte-length cap on an optional
// top-level field. Used for OpenAI / Moonshot tracking identifiers (`user`,
// `safety_identifier`, `prompt_cache_key`, …) that have no inference-side semantics —
// type-check + length cap at the gateway boundary catches garbage payloads early and
// prevents the field from being abused as an unbounded payload carrier under the 10 MiB
// body cap.
type StringFieldValidator struct {
	FieldName     string
	MaxLen        int
	DefaultMaxLen int
}

func (v StringFieldValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document[v.FieldName]
	if !exists {
		return nil
	}
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s: %w: must be a string", v.FieldName, ErrStringFieldShape)
	}
	cap := v.MaxLen
	if cap == 0 {
		cap = v.DefaultMaxLen
	}
	if cap > 0 && len(s) > cap {
		return fmt.Errorf("%s: %w: %d > %d", v.FieldName, ErrStringFieldLength, len(s), cap)
	}
	return nil
}

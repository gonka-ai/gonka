package paramvalidators

import (
	"fmt"
	"math"

	"devshard"
)

// StripParameter deletes the parameter field from the document unconditionally.
type StripParameter struct{}

func (StripParameter) HandleParameter(ctx ParameterContext) error {
	delete(ctx.Document, ctx.Parameter)
	return nil
}

// ConditionalStripParameter deletes the parameter field when Predicate returns true.
// Predicate is called only when the rule fires; nil predicate is a no-op.
type ConditionalStripParameter struct {
	Predicate func(ParameterContext) bool
}

func (h ConditionalStripParameter) HandleParameter(ctx ParameterContext) error {
	if h.Predicate != nil && h.Predicate(ctx) {
		delete(ctx.Document, ctx.Parameter)
	}
	return nil
}

// SanitizeStringListParameter filters a []string-shaped field. Non-string entries pass
// through unchanged so other validators can flag them. DropFieldIfEmpty removes the
// field entirely when the filter leaves it empty.
type SanitizeStringListParameter struct {
	Keep             func(string) bool
	DropFieldIfEmpty bool
}

func (h SanitizeStringListParameter) HandleParameter(ctx ParameterContext) error {
	raw, ok := ctx.Document[ctx.Parameter].([]any)
	if !ok {
		return nil
	}
	cleaned := raw[:0]
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			cleaned = append(cleaned, item)
			continue
		}
		if h.Keep == nil || h.Keep(value) {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 && h.DropFieldIfEmpty {
		delete(ctx.Document, ctx.Parameter)
		return nil
	}
	ctx.Document[ctx.Parameter] = cleaned
	return nil
}

// SanitizeFloatParameter normalizes a numeric knob: parses string/json.Number inputs,
// drops non-finite values when StripNonFinite is set, clamps to [Min, Max].
type SanitizeFloatParameter struct {
	StripNonFinite bool
	Min            *float64
	Max            *float64
}

func (h SanitizeFloatParameter) HandleParameter(ctx ParameterContext) error {
	value, exists := ctx.Document[ctx.Parameter]
	if !exists {
		return nil
	}
	number, ok := devshard.JSONNumericFloat64(value)
	if !ok {
		delete(ctx.Document, ctx.Parameter)
		return nil
	}
	if h.StripNonFinite && (math.IsNaN(number) || math.IsInf(number, 0)) {
		delete(ctx.Document, ctx.Parameter)
		return nil
	}
	if h.Min != nil && number < *h.Min {
		number = *h.Min
	}
	if h.Max != nil && number > *h.Max {
		number = *h.Max
	}
	ctx.Document[ctx.Parameter] = number
	return nil
}

// SanitizeFloatMapParameter applies SanitizeFloatParameter semantics to every value in
// a map-shaped field. Entries that fail clamping are dropped (not clamped) — vLLM
// rejects out-of-range logit_bias entries.
type SanitizeFloatMapParameter struct {
	StripNonFinite   bool
	Min              *float64
	Max              *float64
	DropFieldIfEmpty bool
	MaxEntries       int
}

func (h SanitizeFloatMapParameter) HandleParameter(ctx ParameterContext) error {
	raw, ok := ctx.Document[ctx.Parameter].(map[string]any)
	if !ok {
		return nil
	}
	if h.MaxEntries > 0 && len(raw) > h.MaxEntries {
		return fmt.Errorf("%s: map size %d exceeds limit %d", ctx.Parameter, len(raw), h.MaxEntries)
	}
	for key, value := range raw {
		number, ok := devshard.JSONNumericFloat64(value)
		if !ok {
			continue
		}
		if h.StripNonFinite && (math.IsNaN(number) || math.IsInf(number, 0)) {
			delete(raw, key)
			continue
		}
		if h.Min != nil && number < *h.Min {
			delete(raw, key)
			continue
		}
		if h.Max != nil && number > *h.Max {
			delete(raw, key)
		}
	}
	if len(raw) == 0 && h.DropFieldIfEmpty {
		delete(ctx.Document, ctx.Parameter)
		return nil
	}
	ctx.Document[ctx.Parameter] = raw
	return nil
}

// ForceLiteralParameter writes Value into the document. OverwriteOnly leaves the
// field untouched when it isn't already present.
type ForceLiteralParameter struct {
	Value         any
	OverwriteOnly bool
}

func (h ForceLiteralParameter) HandleParameter(ctx ParameterContext) error {
	if h.OverwriteOnly {
		if _, exists := ctx.Document[ctx.Parameter]; !exists {
			return nil
		}
	}
	ctx.Document[ctx.Parameter] = h.Value
	return nil
}

// CapUintParameter caps a uint64-shaped field to Max.
type CapUintParameter struct {
	Min uint64
	Max uint64
}

func (h CapUintParameter) HandleParameter(ctx ParameterContext) error {
	value, ok := numericJSONValueAsUint64FromMap(ctx.Document, ctx.Parameter)
	if !ok {
		return nil
	}
	if value < h.Min {
		ctx.Document[ctx.Parameter] = h.Min
	}
	if value > h.Max {
		ctx.Document[ctx.Parameter] = h.Max
	}
	return nil
}

// ClampUintToFieldParameter clamps the parameter to the value of another field
// in the same document (e.g. thinking_token_budget ≤ max_tokens). A zero MaxField
// value or missing MaxField is treated as "no clamp".
type ClampUintToFieldParameter struct {
	MaxField string
}

func (h ClampUintToFieldParameter) HandleParameter(ctx ParameterContext) error {
	value, ok := numericJSONValueAsUint64FromMap(ctx.Document, ctx.Parameter)
	if !ok {
		return nil
	}
	maxValue, ok := numericJSONValueAsUint64FromMap(ctx.Document, h.MaxField)
	if !ok || maxValue == 0 {
		return nil
	}
	if value > maxValue {
		ctx.Document[ctx.Parameter] = maxValue
	}
	return nil
}

// ValidateUintParameter rejects requests whose field is present but cannot be parsed
// as a non-negative integer that fits in uint64. Pass-through when the field is absent.
type ValidateUintParameter struct{}

func (h ValidateUintParameter) HandleParameter(ctx ParameterContext) error {
	raw, exists := ctx.Document[ctx.Parameter]
	if !exists || raw == nil {
		return nil
	}
	if _, ok := devshard.JSONNumericUint64(raw); !ok {
		return fmt.Errorf("%s: must be a non-negative integer", ctx.Parameter)
	}
	return nil
}

// RejectNumberParameter rejects the request when the parameter is present and parses as a
// number but Allow returns false for it. Non-numeric or absent values pass through so the
// dedicated type/shape validators own those cases. Used for range gates whose lower bound is
// exclusive (e.g. top_p > 0), where clamping to the bound would itself be an illegal value.
type RejectNumberParameter struct {
	Allow   func(float64) bool
	Message string
}

func (h RejectNumberParameter) HandleParameter(ctx ParameterContext) error {
	value, exists := ctx.Document[ctx.Parameter]
	if !exists {
		return nil
	}
	number, ok := devshard.JSONNumericFloat64(value)
	if !ok {
		return nil
	}
	if h.Allow == nil || h.Allow(number) {
		return nil
	}
	return fmt.Errorf("%s: %s", ctx.Parameter, h.Message)
}

// ValidateScalarParameter rejects the request when the parameter is present (non-null) but
// Valid returns false for its raw JSON value. Catches wrong-typed scalars at the boundary
// instead of forwarding them for an opaque upstream 400.
type ValidateScalarParameter struct {
	Valid   func(any) bool
	Message string
}

func (h ValidateScalarParameter) HandleParameter(ctx ParameterContext) error {
	value, exists := ctx.Document[ctx.Parameter]
	if !exists || value == nil {
		return nil
	}
	if h.Valid == nil || h.Valid(value) {
		return nil
	}
	return fmt.Errorf("%s: %s", ctx.Parameter, h.Message)
}

// ValidateListElementsParameter rejects the request when any array element fails Valid.
// Pass-through when the field is absent or not an array.
type ValidateListElementsParameter struct {
	Valid   func(any) bool
	Message string
}

func (h ValidateListElementsParameter) HandleParameter(ctx ParameterContext) error {
	raw, ok := ctx.Document[ctx.Parameter].([]any)
	if !ok {
		return nil
	}
	for index, item := range raw {
		if h.Valid != nil && !h.Valid(item) {
			return fmt.Errorf("%s[%d]: %s", ctx.Parameter, index, h.Message)
		}
	}
	return nil
}

// JSON type predicates for the Validate*Parameter handlers above.
func IsJSONBool(value any) bool   { _, ok := value.(bool); return ok }
func IsJSONString(value any) bool { _, ok := value.(string); return ok }
func IsJSONUint(value any) bool   { _, ok := devshard.JSONNumericUint64(value); return ok }

// LengthCapListParameter bounds the number of entries in an array field, and
// optionally the byte length of each string entry. MaxEntries=0 disables the
// array cap; MaxEntryLen=0 disables the per-string cap.
type LengthCapListParameter struct {
	MaxEntries  int
	MaxEntryLen int
}

func (h LengthCapListParameter) HandleParameter(ctx ParameterContext) error {
	raw, ok := ctx.Document[ctx.Parameter].([]any)
	if !ok {
		return nil
	}
	if h.MaxEntries > 0 && len(raw) > h.MaxEntries {
		return fmt.Errorf("%s: array length %d exceeds limit %d", ctx.Parameter, len(raw), h.MaxEntries)
	}
	if h.MaxEntryLen > 0 {
		for i, item := range raw {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if len(s) > h.MaxEntryLen {
				return fmt.Errorf("%s[%d]: string length %d exceeds limit %d", ctx.Parameter, i, len(s), h.MaxEntryLen)
			}
		}
	}
	return nil
}

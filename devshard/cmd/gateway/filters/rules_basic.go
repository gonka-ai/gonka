package filters

import (
	"math"
	"regexp"
	"strings"

	"devshard"
)

var modelNameRegex = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// requireUint rejects ctx.Param when present and not a non-negative integer; absent/null pass.
func requireUint() RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get(ctx.Param)
		if !presentField(raw, exists) {
			return nil
		}
		if _, ok := devshard.JSONNumericUint64(raw); !ok {
			return Reject("%s: must be a non-negative integer", ctx.Param)
		}
		return nil
	}
}

// requireBool rejects ctx.Param when present and not a boolean; absent/null pass.
func requireBool() RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get(ctx.Param)
		if !presentField(raw, exists) {
			return nil
		}
		if _, ok := raw.(bool); !ok {
			return Reject("%s: must be a boolean", ctx.Param)
		}
		return nil
	}
}

// requireString rejects a non-string or over-long ctx.Param; unlike requireUint/requireBool, null is rejected.
func requireString(maxBytes int) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get(ctx.Param)
		if !exists {
			return nil
		}
		value, ok := raw.(string)
		if !ok {
			return Reject("%s: invalid wrapper shape: must be a string", ctx.Param)
		}
		if len(value) > maxBytes {
			return Reject("%s: length exceeded: %d > %d", ctx.Param, len(value), maxBytes)
		}
		return nil
	}
}

// validModelName rejects a non-string, blank, over-long, or non-matching ctx.Param; absent passes through.
func validModelName(maxBytes int) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get(ctx.Param)
		if !exists {
			return nil
		}
		value, ok := raw.(string)
		if !ok {
			return Reject("%s: invalid wrapper shape: must be a string", ctx.Param)
		}
		if len(value) > maxBytes {
			return Reject("%s: length exceeded: %d > %d", ctx.Param, len(value), maxBytes)
		}
		if strings.TrimSpace(value) == "" {
			return Reject("%s: must not be empty", ctx.Param)
		}
		if !modelNameRegex.MatchString(value) {
			return Reject("%s: invalid: must contain only letters, digits, and the characters ._-/", ctx.Param)
		}
		return nil
	}
}

func stripParameter() RuleFunc {
	return func(ctx RuleContext) error {
		ctx.Document.Delete(ctx.Param)
		return nil
	}
}

func capUint(max uint64) RuleFunc {
	return func(ctx RuleContext) error {
		raw, exists := ctx.Document.Get(ctx.Param)
		if !presentField(raw, exists) {
			return nil
		}
		asked, isNumber := devshard.JSONNumericUint64(raw)
		if !isNumber || asked <= max {
			return nil
		}
		ctx.Document.Set(ctx.Param, max)
		return nil
	}
}

func forceLiteral(value any) RuleFunc {
	return func(ctx RuleContext) error {
		ctx.Document.Set(ctx.Param, value)
		return nil
	}
}

func replaceIfPresent(value any) RuleFunc {
	return func(ctx RuleContext) error {
		if ctx.Document.Has(ctx.Param) {
			ctx.Document.Set(ctx.Param, value)
		}
		return nil
	}
}

// clampFloat coerces ctx.Param to float64 via sanitizeFloatField, then clamps it into [min, max].
func clampFloat(min, max float64) RuleFunc {
	return func(ctx RuleContext) error {
		number, ok := sanitizeFloatField(ctx.Document, ctx.Param)
		if !ok {
			return nil
		}
		if number < min {
			number = min
		}
		if number > max {
			number = max
		}
		ctx.Document.Set(ctx.Param, number)
		return nil
	}
}

// rejectNonPositiveThenClamp clamps down to max first: an exclusive lower bound can't be clamped to a legal value.
func rejectNonPositiveThenClamp(max float64) RuleFunc {
	return func(ctx RuleContext) error {
		number, ok := sanitizeFloatField(ctx.Document, ctx.Param)
		if !ok {
			return nil
		}
		if number > max {
			number = max
		}
		if number <= 0 {
			return Reject("%s: must be greater than 0", ctx.Param)
		}
		ctx.Document.Set(ctx.Param, number)
		return nil
	}
}

// validTopK rejects anything but -1 (disabled) or a value >= 1, then clamps down to max and truncates.
func validTopK(max float64) RuleFunc {
	return func(ctx RuleContext) error {
		number, ok := sanitizeFloatField(ctx.Document, ctx.Param)
		if !ok {
			return nil
		}
		if number != topKDisabled && number < 1 {
			return Reject("%s: must be -1 or a positive integer", ctx.Param)
		}
		if number > max {
			number = max
		}
		ctx.Document.Set(ctx.Param, int64(math.Trunc(number)))
		return nil
	}
}

// sanitizeFloatField coerces document[name] to float64, deleting it when unparseable or non-finite (NaN/Inf).
func sanitizeFloatField(document *Document, name string) (float64, bool) {
	raw, exists := document.Get(name)
	if !exists {
		return 0, false
	}
	number, ok := devshard.JSONNumericFloat64(raw)
	if !ok || nonFinite(number) {
		document.Delete(name)
		return 0, false
	}
	return number, true
}

// nonFinite reports NaN or an infinity, neither of which is forwarded to a host.
func nonFinite(number float64) bool {
	return math.IsNaN(number) || math.IsInf(number, 0)
}

// IsValidModelName applies the request rule for `model` to a name that arrives elsewhere, such as an admin path.
func IsValidModelName(name string) bool {
	return len(name) <= modelMaxLen && strings.TrimSpace(name) != modelNameBlank && modelNameRegex.MatchString(name)
}

package filters

import (
	"strconv"
	"strings"

	"devshard"
)

// streamOptionsWhitelist is the only stream_options sub-field forwarded upstream.
var streamOptionsWhitelist = map[string]struct{}{"include_usage": {}}

// validListLength rejects an over-long array or (when maxEntryLen > 0) an over-long string element.
func validListLength(maxEntries, maxEntryLen int) RuleFunc {
	return func(ctx RuleContext) error {
		list, ok := ctx.Document.Array(ctx.Param)
		if !ok {
			return nil
		}
		if maxEntries > 0 && len(list) > maxEntries {
			return Reject("%s: array length %d exceeds limit %d", ctx.Param, len(list), maxEntries)
		}
		if maxEntryLen > 0 {
			for index, item := range list {
				value, ok := item.(string)
				if !ok || len(value) <= maxEntryLen {
					continue
				}
				return Reject("%s[%d]: string length %d exceeds limit %d", ctx.Param, index, len(value), maxEntryLen)
			}
		}
		return nil
	}
}

// requireListElements rejects the first array element failing valid, as "<param>[<index>]: <message>".
func requireListElements(valid func(any) bool, message string) RuleFunc {
	return func(ctx RuleContext) error {
		list, ok := ctx.Document.Array(ctx.Param)
		if !ok {
			return nil
		}
		for index, item := range list {
			if !valid(item) {
				return Reject("%s[%d]: %s", ctx.Param, index, message)
			}
		}
		return nil
	}
}

func requireStringElements() RuleFunc {
	return requireListElements(isJSONString, "must be a string")
}

func isJSONString(value any) bool { _, ok := value.(string); return ok }

// dropBlankStringListElements removes whitespace-only strings, dropping the field when nothing survives.
func dropBlankStringListElements() RuleFunc {
	return func(ctx RuleContext) error {
		list, ok := ctx.Document.Array(ctx.Param)
		if !ok {
			return nil
		}
		cleaned := list[:0]
		for _, item := range list {
			value, ok := item.(string)
			if !ok {
				cleaned = append(cleaned, item)
				continue
			}
			if strings.TrimSpace(value) != "" {
				cleaned = append(cleaned, value)
			}
		}
		if len(cleaned) == 0 {
			ctx.Document.Delete(ctx.Param)
			return nil
		}
		ctx.Document.Set(ctx.Param, cleaned)
		return nil
	}
}

// requireTokenIDKeys rejects the lexicographically first key that is not a non-negative token id (vLLM #16529).
func requireTokenIDKeys() RuleFunc {
	return func(ctx RuleContext) error {
		object, ok := ctx.Document.Object(ctx.Param)
		if !ok {
			return nil
		}
		invalidKey, found := "", false
		for key := range object {
			if tokenID, err := strconv.ParseInt(key, 10, 32); err == nil && tokenID >= 0 {
				continue
			}
			if !found || key < invalidKey {
				invalidKey, found = key, true
			}
		}
		if found {
			return Reject("%s: key invalid: %q is not a non-negative integer token id", ctx.Param, invalidKey)
		}
		return nil
	}
}

// validFloatMap rejects past maxEntries, drops out-of-range or non-finite entries, and drops an emptied field.
func validFloatMap(min, max float64, maxEntries int) RuleFunc {
	return func(ctx RuleContext) error {
		object, ok := ctx.Document.Object(ctx.Param)
		if !ok {
			return nil
		}
		if maxEntries > 0 && len(object) > maxEntries {
			return Reject("%s: map size %d exceeds limit %d", ctx.Param, len(object), maxEntries)
		}
		for key, value := range object {
			number, ok := devshard.JSONNumericFloat64(value)
			if !ok {
				continue
			}
			if nonFinite(number) || number < min || number > max {
				delete(object, key)
			}
		}
		if len(object) == 0 {
			ctx.Document.Delete(ctx.Param)
			return nil
		}
		ctx.Document.Set(ctx.Param, object)
		return nil
	}
}

// validMetadata enforces the OpenAI-compatible metadata contract. See README.md, "Parameter rules".
func validMetadata(maxKeys, maxKeyLen, maxValueLen int) RuleFunc {
	return func(ctx RuleContext) error {
		object, present, isObject := ctx.Document.ObjectField(ctx.Param)
		if !present {
			return nil
		}
		if !isObject {
			return Reject("%s: invalid wrapper shape: must be an object", ctx.Param)
		}
		if len(object) > maxKeys {
			return Reject("%s: key count exceeded: %d > %d", ctx.Param, len(object), maxKeys)
		}
		for key, value := range object {
			if len(key) > maxKeyLen {
				return Reject("%s: key invalid: key length %d > %d", ctx.Param, len(key), maxKeyLen)
			}
			stringValue, ok := value.(string)
			if !ok {
				return Reject("%s: value invalid: value for %q must be a string", ctx.Param, key)
			}
			if len(stringValue) > maxValueLen {
				return Reject("%s: value invalid: value for %q length %d > %d", ctx.Param, key, len(stringValue), maxValueLen)
			}
		}
		return nil
	}
}

// validStreamOptions strips ctx.Param unless stream is exactly true, then keeps only whitelisted sub-fields.
func validStreamOptions() RuleFunc {
	return func(ctx RuleContext) error {
		object, present, isObject := ctx.Document.ObjectField(ctx.Param)
		if !present {
			return nil
		}
		streamValue, _ := ctx.Document.Get("stream")
		if streaming, ok := streamValue.(bool); !ok || !streaming {
			ctx.Document.Delete(ctx.Param)
			return nil
		}
		if !isObject {
			return Reject("%s: invalid wrapper shape: must be an object", ctx.Param)
		}
		for key := range object {
			if _, allowed := streamOptionsWhitelist[key]; !allowed {
				delete(object, key)
			}
		}
		if len(object) == 0 {
			ctx.Document.Delete(ctx.Param)
			return nil
		}
		ctx.Document.Set(ctx.Param, object)
		return nil
	}
}

package filters

import (
	stdjson "encoding/json"
	"errors"
	"strconv"
)

const cachedTokensField = "cached_tokens"

func boundCachedTokens(decoded any, promptTokens uint64) (carried bool) {
	event, isObject := decoded.(map[string]any)
	if !isObject {
		return false
	}
	usage, isObject := event["usage"].(map[string]any)
	if !isObject {
		return false
	}
	details, isObject := usage["prompt_tokens_details"].(map[string]any)
	if !isObject {
		return false
	}
	reported, held := details[cachedTokensField]
	if !held || reported == nil {
		return held
	}
	number, isNumber := reported.(stdjson.Number)
	if !isNumber {
		delete(details, cachedTokensField)
		return true
	}
	// A range error still returns the nearest int64, which the clamp below bounds.
	cached, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		delete(details, cachedTokensField)
		return true
	}
	details[cachedTokensField] = stdjson.Number(strconv.FormatUint(min(uint64(max(cached, 0)), promptTokens), 10))
	return true
}

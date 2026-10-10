package completionapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"

	"common/logging"
)

// ForcedTopLogprobs is the width devshard pins on every executed request, so executor and validator logprobs stay comparable (H1 #3853145).
const ForcedTopLogprobs = 5

type ModifiedRequest struct {
	NewBody         []byte
	AsksForLogprobs bool
}

// MinTokensFloor is the output budget of the gateway PoC probe.
const MinTokensFloor = 64

func ModifyRequestBody(requestBytes []byte, defaultSeed int32) (*ModifiedRequest, error) {
	return ModifyRequestBodyWithLogprobsMode(requestBytes, defaultSeed, "")
}

func ModifyRequestBodyWithLogprobsMode(requestBytes []byte, defaultSeed int32, logprobsMode string) (*ModifiedRequest, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}
	if err := validateOpenAICompatRequestMap(requestMap); err != nil {
		return nil, err
	}

	if err := validateMessageContents(requestMap); err != nil {
		return nil, err
	}

	asksForLogprobs := logprobsAsked(requestMap)
	// Pin both fields: anything the engine reads as logprobs-off leaves the inference unvalidatable.
	requestMap["logprobs"] = true
	requestMap["top_logprobs"] = ForcedTopLogprobs

	EnforceTokenBudget(requestMap)

	// Only clamp when the caller asked: injecting n into a request that never
	// carried it would change the body we sign for a broker that never set it.
	if _, asked := requestMap["n"]; asked {
		requestMap["n"] = 1
	}
	requestMap["skip_special_tokens"] = false
	requestMap["return_token_ids"] = true
	if _, ok := requestMap["seed"]; !ok {
		requestMap["seed"] = defaultSeed
	}

	// Use safe type assertion to avoid panic on malformed input
	if doStream, ok := requestMap["stream"]; ok {
		if doStreamBool, isBool := doStream.(bool); isBool && doStreamBool {
			if streamOpts, exists := requestMap["stream_options"]; !exists {
				requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
			} else if streamOptsMap, isMap := streamOpts.(map[string]interface{}); isMap {
				streamOptsMap["include_usage"] = true
			} else {
				// stream_options exists but is not a map - replace with valid map
				logging.Warn("Malformed stream_options field received, replacing with defaults",
					types.Inferences, "stream_options_value", fmt.Sprintf("%v", streamOpts))
				requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
			}
		}
	}

	if logprobsMode != "" {
		delete(requestMap, "logprobs_mode")
		requestMap["logprobs_mode"] = logprobsMode
	}

	modifiedRequestBytes, err := json.Marshal(requestMap)
	if err != nil {
		return nil, err
	}

	return &ModifiedRequest{
		NewBody:         modifiedRequestBytes,
		AsksForLogprobs: asksForLogprobs,
	}, nil
}

func validateMessageContents(requestMap map[string]interface{}) error {
	rawMessages, ok := requestMap["messages"]
	if !ok || rawMessages == nil {
		return nil
	}

	messages, ok := rawMessages.([]interface{})
	if !ok {
		return fmt.Errorf("messages must be an array")
	}

	for i, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			return fmt.Errorf("messages[%d] must be an object", i)
		}

		content, exists := message["content"]
		if !exists {
			continue
		}
		if content == nil {
			continue
		}

		switch typedContent := content.(type) {
		case string:
			continue
		case []interface{}:
			for j, rawPart := range typedContent {
				part, ok := rawPart.(map[string]interface{})
				if !ok {
					return fmt.Errorf("messages[%d].content[%d] must be an object", i, j)
				}

				partType, ok := part["type"].(string)
				if !ok || partType == "" {
					return fmt.Errorf("messages[%d].content[%d].type must be a string", i, j)
				}

				// TODO(vision-costs): We currently validate and pass through non-text parts
				// (e.g. image_url) but downstream prompt token accounting still often uses
				// flattened text-only content. This can underfund/gas-underprice vision
				// requests. Future fix: include non-text token costs in promptTokenCount
				// before transaction construction.
				if partType != "text" {
					continue
				}

				rawText, exists := part["text"]
				if !exists {
					return fmt.Errorf("messages[%d].content[%d].text is required for type %q", i, j, partType)
				}

				text, ok := rawText.(string)
				if !ok {
					return fmt.Errorf("messages[%d].content[%d].text must be a string", i, j)
				}
				if text == "" {
					return fmt.Errorf("messages[%d].content[%d].text must be a non-empty string", i, j)
				}
			}
		default:
			return fmt.Errorf("messages[%d].content must be a string or an array of typed content parts", i)
		}
	}

	return nil
}

// EnforceTokenBudget pins max_tokens/max_completion_tokens to one value and keeps only a caller
// min_tokens, clamped to it. stop_token_ids pass through: check them with ValidateStopTokenIDs.
// See devshard/docs/proposals/short-output-validation.md.
func EnforceTokenBudget(requestMap map[string]interface{}) {
	maxTokens := getMaxTokens(requestMap)
	requestMap["max_tokens"] = maxTokens
	requestMap["max_completion_tokens"] = maxTokens
	if minTokens := getMinTokens(requestMap); minTokens > 0 {
		requestMap["min_tokens"] = min(minTokens, maxTokens)
	} else {
		delete(requestMap, "min_tokens")
	}
}

// ErrStopTokenIDs marks a stop_token_ids field the engine must not be sent.
var ErrStopTokenIDs = errors.New("invalid stop_token_ids")

// ValidateStopTokenIDs checks every stop_token_ids entry is an integer in [0, vocabularySize).
// An unknown vocabulary (vocabularySize <= 0) fails closed: with min_tokens > 0 the engine indexes
// the logits by these ids, so an out-of-range id crashes the node rather than being ignored.
func ValidateStopTokenIDs(requestMap map[string]interface{}, vocabularySize int) error {
	raw, present := requestMap["stop_token_ids"]
	if !present || raw == nil {
		return nil
	}
	ids, isArray := raw.([]interface{})
	if !isArray {
		return fmt.Errorf("%w: must be an array of token ids", ErrStopTokenIDs)
	}
	if len(ids) == 0 {
		return nil
	}
	if vocabularySize <= 0 {
		return fmt.Errorf("%w: model vocabulary size unknown", ErrStopTokenIDs)
	}
	for index, entry := range ids {
		id, isID := tokenIDValue(entry)
		if !isID || id < 0 || id >= int64(vocabularySize) {
			return fmt.Errorf("%w: entry %d is not a token id in [0, %d)", ErrStopTokenIDs, index, vocabularySize)
		}
	}
	return nil
}

// StopTokenIDsOf returns the stop ids the request carries, as the decimal strings logprobs use.
func StopTokenIDsOf(requestMap map[string]interface{}) map[string]struct{} {
	ids, _ := requestMap["stop_token_ids"].([]interface{})
	if len(ids) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, entry := range ids {
		if id, isID := tokenIDValue(entry); isID {
			set[strconv.FormatInt(id, 10)] = struct{}{}
		}
	}
	return set
}

func tokenIDValue(entry interface{}) (int64, bool) {
	switch value := entry.(type) {
	case float64:
		if value != math.Trunc(value) || math.Abs(value) > 1<<53 {
			return 0, false
		}
		return int64(value), true
	case int:
		return int64(value), true
	case int64:
		return value, true
	case json.Number:
		id, err := value.Int64()
		return id, err == nil
	default:
		return 0, false
	}
}

func getMinTokens(requestMap map[string]interface{}) int {
	minTokensValue, ok := requestMap["min_tokens"]
	if !ok {
		return 0
	}
	switch value := minTokensValue.(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func getMaxTokens(requestMap map[string]interface{}) int {
	if maxTokensValue, ok := requestMap["max_tokens"]; ok {
		if maxTokensFloat, ok := maxTokensValue.(float64); ok {
			return int(maxTokensFloat)
		}
		if maxTokensInt, ok := maxTokensValue.(int); ok {
			return maxTokensInt
		}
	}
	if maxCompletionTokensValue, ok := requestMap["max_completion_tokens"]; ok {
		if maxCompletionTokensFloat, ok := maxCompletionTokensValue.(float64); ok {
			return int(maxCompletionTokensFloat)
		}
		if maxCompletionTokensInt, ok := maxCompletionTokensValue.(int); ok {
			return maxCompletionTokensInt
		}
	}
	return calculations.DefaultMaxTokens // Default value if not specified
}

// EffectiveMaxTokens returns the output-token limit the ML execution path will
// apply for this request body: explicit max_tokens / max_completion_tokens when
// present, otherwise calculations.DefaultMaxTokens.
func EffectiveMaxTokens(requestBytes []byte) (uint64, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return 0, err
	}
	return uint64(getMaxTokens(requestMap)), nil
}

// MinTokensOf returns the min_tokens the request carries (0 if unset).
func MinTokensOf(requestMap map[string]interface{}) int {
	return getMinTokens(requestMap)
}

// LogprobsAsked reads the pair as one intent: logprobs alone names no width, and a width of zero switches them off.
func LogprobsAsked(logprobs bool, topLogprobs float64) bool {
	return logprobs && topLogprobs > 0
}

// Only an explicit boolean asks; the forcing above then overwrites whatever the caller wrote.
func logprobsAsked(requestMap map[string]interface{}) bool {
	asked, isBool := requestMap["logprobs"].(bool)
	width, isNumber := requestMap["top_logprobs"].(float64)
	return isBool && isNumber && LogprobsAsked(asked, width)
}

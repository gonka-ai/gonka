package completionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
)

var droppedLogprobFields = []string{"bytes", "logprob"}

// CompressResponsePayload drops those fields from a stored response, streamed envelope or plain completion.
// Numbers travel as json.Number, so a value it keeps is re-emitted with the digits it arrived with.
func CompressResponsePayload(payload []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("compress payload: %w", err)
	}
	if err := compressLogprobsIn(document); err != nil {
		return nil, err
	}
	compressed, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("compress payload: %w", err)
	}
	return compressed, nil
}

func compressLogprobsIn(node any) error {
	switch typed := node.(type) {
	case map[string]any:
		if content, ok := typed["logprobs"].(map[string]any); ok {
			if err := compressLogprobContent(content["content"]); err != nil {
				return err
			}
		}
		for _, child := range typed {
			if err := compressLogprobsIn(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := compressLogprobsIn(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func compressLogprobContent(content any) error {
	positions, ok := content.([]any)
	if !ok {
		return nil
	}
	for index, raw := range positions {
		position, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := verifyPositionCompressible(index, position); err != nil {
			return err
		}
		for _, field := range droppedLogprobFields {
			delete(position, field)
		}
		alternatives, ok := position["top_logprobs"].([]any)
		if !ok {
			continue
		}
		for _, raw := range alternatives {
			if alternative, ok := raw.(map[string]any); ok {
				delete(alternative, "bytes")
			}
		}
	}
	return nil
}

func verifyPositionCompressible(index int, position map[string]any) error {
	alternatives, ok := position["top_logprobs"].([]any)
	if !ok || len(alternatives) == 0 {
		return nil
	}
	token, _ := position["token"].(string)
	matched := false
	for rank, raw := range alternatives {
		alternative, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		alternativeToken, _ := alternative["token"].(string)
		if spelled, present := alternative["bytes"].([]any); present && len(spelled) > 0 {
			if !bytesSpellToken(spelled, alternativeToken) {
				return fmt.Errorf("logprobs position %d rank %d: bytes do not spell token %q", index, rank, alternativeToken)
			}
		}
		if alternativeToken == token && sameNumber(alternative["logprob"], position["logprob"]) {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("logprobs position %d: no alternative explains the logprob of token %q", index, token)
	}
	return nil
}

func bytesSpellToken(spelled []any, token string) bool {
	if len(spelled) != len(token) {
		return false
	}
	for index, raw := range spelled {
		number, ok := raw.(json.Number)
		if !ok {
			return false
		}
		value, err := number.Int64()
		if err != nil || value != int64(token[index]) {
			return false
		}
	}
	return true
}

func sameNumber(left, right any) bool {
	leftNumber, leftOK := left.(json.Number)
	rightNumber, rightOK := right.(json.Number)
	if !leftOK || !rightOK {
		return false
	}
	leftValue, leftErr := leftNumber.Float64()
	rightValue, rightErr := rightNumber.Float64()
	return leftErr == nil && rightErr == nil && leftValue == rightValue
}

package completionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

var droppedLogprobFields = []string{"bytes", "logprob"}

// fieldsNoValidatorReads are the serving engine's own bookkeeping. Nothing in validation reads them
// and the gateway drops all three on arrival (internalStrippedFields, devshardctl/stream_rewrite.go),
// so the hashed payload carries them for nothing.
var fieldsNoValidatorReads = []string{"token_ids", "prompt_token_ids", "prompt_logprobs"}

// CompressResponsePayload slims a whole stored response, streamed envelope or plain completion. The
// executor slims chunk by chunk as it parses them, so this is the entry point for a payload nobody
// parsed on the way in -- a backfill over what is already on disk. Running it twice is a no-op.
func CompressResponsePayload(payload []byte) ([]byte, error) {
	document, err := decodeJSONDocument(payload)
	if err != nil {
		return nil, fmt.Errorf("compress payload: %w", err)
	}
	if err := SlimStoredDocument(document); err != nil {
		return nil, err
	}
	compressed, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("compress payload: %w", err)
	}
	return compressed, nil
}

// SlimStoredDocument drops them from a response already decoded, so a caller that parsed the body
// once does not pay for a second pass. A streamed envelope keeps its chunks as JSON strings, which
// an ordinary walk steps over, so its events are slimmed one by one.
func SlimStoredDocument(document any) error {
	if events, isEnvelope := streamedEnvelopeEvents(document); isEnvelope {
		slimStreamedEvents(events)
		return nil
	}
	dropFields(document, fieldsNoValidatorReads)
	return compressLogprobsIn(document)
}

func decodeJSONDocument(payload []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	return document, nil
}

func streamedEnvelopeEvents(document any) ([]any, bool) {
	object, isObject := document.(map[string]any)
	if !isObject {
		return nil, false
	}
	events, isEnvelope := object["events"].([]any)
	return events, isEnvelope
}

func slimStreamedEvents(events []any) {
	for index, raw := range events {
		line, isString := raw.(string)
		if !isString {
			continue
		}
		slimmed, err := slimStreamedLine(line)
		if err != nil {
			// One chunk a host wrote inconsistently keeps its fields; the rest of the stream still slims.
			continue
		}
		events[index] = slimmed
	}
}

// slimStreamedLine hands back anything that is not a JSON data line untouched, so [DONE] and a
// host's malformed chunk survive the walk as they arrived.
func slimStreamedLine(line string) (string, error) {
	body, isData := streamedLineBody(line)
	if !isData {
		return line, nil
	}
	document, err := decodeJSONDocument([]byte(body))
	if err != nil {
		return line, nil
	}
	if err := SlimStoredDocument(document); err != nil {
		return "", err
	}
	slimmed, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("compress payload: %w", err)
	}
	return DataPrefix + string(slimmed), nil
}

func streamedLineBody(line string) (string, bool) {
	if !strings.HasPrefix(line, DataPrefix) {
		return "", false
	}
	body := strings.TrimSpace(strings.TrimPrefix(line, DataPrefix))
	if body == "" || strings.HasPrefix(body, "[DONE]") {
		return "", false
	}
	return body, true
}

func dropFields(node any, fields []string) {
	switch typed := node.(type) {
	case map[string]any:
		for _, field := range fields {
			delete(typed, field)
		}
		for _, child := range typed {
			dropFields(child, fields)
		}
	case []any:
		for _, child := range typed {
			dropFields(child, fields)
		}
	}
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
		// A position with no logprob of its own was already slimmed, so a second pass has nothing to
		// verify and nothing to drop. Without this a re-run fails on its own output.
		if _, unslimmed := position["logprob"]; !unslimmed {
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

package completionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Above this size, skipping a prompt-wide array's decode beats a second look at the bytes.
const skipDecodeAboveBytes = 4 << 10

var droppedLogprobFields = []string{"bytes", "logprob"}

// fieldsNoValidatorReads are the serving engine's own bookkeeping. Nothing in validation reads them
// and the gateway drops all three on arrival (internalStrippedFields, devshardctl/stream_rewrite.go),
// so the hashed payload carries them for nothing.
var fieldsNoValidatorReads = []string{"token_ids", "prompt_token_ids", "prompt_logprobs"}

// fieldsOnlyAskingCallersSee is what a caller that did not ask for logprobs must not be sent.
var fieldsOnlyAskingCallersSee = []string{"logprobs"}

// The same names, quoted once, for a scan that runs before the decode.
var quotedFieldsNoValidatorReads = quoteFieldNames(fieldsNoValidatorReads)

func quoteFieldNames(fields []string) [][]byte {
	quoted := make([][]byte, len(fields))
	for index, field := range fields {
		quoted[index] = []byte(`"` + field + `"`)
	}
	return quoted
}

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

// decodeDocumentWithoutUnreadFields keeps a prompt-wide array as bytes, not as decoded values.
func decodeDocumentWithoutUnreadFields(payload []byte) (any, error) {
	if len(payload) < skipDecodeAboveBytes || !carriesUnreadField(payload) {
		return decodeJSONDocument(payload)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return decodeJSONDocument(payload)
	}
	for _, field := range fieldsNoValidatorReads {
		delete(fields, field)
	}
	document := make(map[string]any, len(fields))
	for key, value := range fields {
		decoded, err := decodeJSONDocument(value)
		if err != nil {
			return nil, err
		}
		document[key] = decoded
	}
	return document, nil
}

func carriesUnreadField(payload []byte) bool {
	for _, field := range quotedFieldsNoValidatorReads {
		if bytes.Contains(payload, field) {
			return true
		}
	}
	return false
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

// compressLogprobsIn checks every position before it strips any, so a refused document is left as it arrived.
func compressLogprobsIn(node any) error {
	compressible, err := verifiedLogprobPositions(node, nil)
	if err != nil {
		return err
	}
	for _, position := range compressible {
		stripPosition(position)
	}
	return nil
}

// verifiedLogprobPositions gathers what still has to be slimmed; one with no logprob of its own was already done.
func verifiedLogprobPositions(node any, found []map[string]any) ([]map[string]any, error) {
	switch typed := node.(type) {
	case map[string]any:
		if content, isObject := typed["logprobs"].(map[string]any); isObject {
			positions, isArray := content["content"].([]any)
			if isArray {
				for index, raw := range positions {
					position, isObject := raw.(map[string]any)
					if !isObject {
						continue
					}
					if _, unslimmed := position["logprob"]; !unslimmed {
						continue
					}
					if err := verifyPositionCompressible(index, position); err != nil {
						return nil, err
					}
					found = append(found, position)
				}
			}
		}
		for _, child := range typed {
			var err error
			if found, err = verifiedLogprobPositions(child, found); err != nil {
				return nil, err
			}
		}
	case []any:
		for _, child := range typed {
			var err error
			if found, err = verifiedLogprobPositions(child, found); err != nil {
				return nil, err
			}
		}
	}
	return found, nil
}

func stripPosition(position map[string]any) {
	for _, field := range droppedLogprobFields {
		delete(position, field)
	}
	alternatives, isArray := position["top_logprobs"].([]any)
	if !isArray {
		return
	}
	for _, raw := range alternatives {
		if alternative, isObject := raw.(map[string]any); isObject {
			delete(alternative, "bytes")
		}
	}
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

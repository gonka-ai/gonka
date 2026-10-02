package completionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ErrorDetails is the OpenAI-style top-level error extracted from a stored
// response payload. Fields are empty when no such object is present.
type ErrorDetails struct {
	Type    string
	Code    string
	Message string
}

// IsTerminalErrorResponse reports whether a stored response payload is not a
// usable completion — an error envelope and/or malformed `data:` JSON.
//
// This is the consensus rule for MsgErrorMiss. Every host must compile against
// this one implementation. There is no independent backstop: verifiers run the
// same predicate over the same hash-pinned bytes as the gateway, so a
// misclassification is agreed, not caught.
//
// The host fully controls the payload. A signed Finish over an error envelope
// is the proof, including when content tokens precede the error. Unparseable
// extra `data:` JSON is also a miss (the junk is the proof), not a veto.
//
// Accept when the payload is a streamed `{"events":[...]}` body and either:
//   - some event carries a top-level error object (the {"error":{code,message,type}}
//     shape, matching the gateway's sseChunkErrorPayload), regardless of prior
//     content or usage.completion_tokens; or
//   - some `data:` line is not valid JSON.
//
// Lines are taken from the serialized envelope so a junk `data:` event cannot
// veto the miss by making NewCompletionResponseFromLines fail closed.
func IsTerminalErrorResponse(responsePayload []byte) (details ErrorDetails, ok bool) {
	if len(responsePayload) == 0 {
		return ErrorDetails{}, false
	}
	var serialized SerializedStreamedResponse
	if err := json.Unmarshal(responsePayload, &serialized); err != nil || len(serialized.Events) == 0 {
		return ErrorDetails{}, false
	}
	details, hasError := terminalErrorFromLines(serialized.Events)
	if hasError {
		return details, true
	}
	if streamedLinesHaveUnparseableData(serialized.Events) {
		return ErrorDetails{}, true
	}
	return ErrorDetails{}, false
}

func terminalErrorFromLines(lines []string) (ErrorDetails, bool) {
	for _, line := range lines {
		payload, ok := sseDataJSON(line)
		if !ok {
			continue
		}
		var evt struct {
			Error *struct {
				Type    string `json:"type"`
				Code    any    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Object  string `json:"object"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(payload, &evt); err != nil {
			continue
		}
		if evt.Error != nil {
			return ErrorDetails{
				Type:    evt.Error.Type,
				Code:    stringifyErrorCode(evt.Error.Code),
				Message: evt.Error.Message,
			}, true
		}
		if evt.Object == "error" && evt.Message != "" {
			return ErrorDetails{
				Type:    evt.Type,
				Code:    stringifyErrorCode(evt.Code),
				Message: evt.Message,
			}, true
		}
	}
	return ErrorDetails{}, false
}

func stringifyErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

func sseDataJSON(line string) ([]byte, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return nil, false
	}
	return []byte(payload), true
}

func streamedLinesHaveUnparseableData(lines []string) bool {
	for _, line := range lines {
		payload, ok := sseDataJSON(line)
		if !ok {
			continue
		}
		if !json.Valid(payload) {
			return true
		}
	}
	return false
}

func IsClientFaultError(details ErrorDetails) bool {
	switch strings.TrimSpace(details.Code) {
	case "400", "422":
		return true
	}
	return false
}

func IsClientFaultErrorResponse(responsePayload []byte) bool {
	var serialized SerializedStreamedResponse
	if err := json.Unmarshal(responsePayload, &serialized); err != nil || len(serialized.Events) == 0 {
		return false
	}
	if streamedLinesHaveUnparseableData(serialized.Events) {
		return false
	}
	foundClientFault := false
	for _, line := range serialized.Events {
		payload, ok := sseDataJSON(line)
		if !ok {
			continue
		}
		if chunkCarriesOutput(payload) {
			return false
		}
		if details, isError := terminalErrorFromLines([]string{line}); isError {
			if !IsClientFaultError(details) {
				return false
			}
			foundClientFault = true
		}
	}
	return foundClientFault
}

const (
	MaxClientFaultBodyBytes    = 16 << 10
	maxClientFaultMessageBytes = 8 << 10
	maxClientFaultTypeBytes    = 256
)

type compactClientFaultError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

type compactClientFault struct {
	Error compactClientFaultError `json:"error"`
}

func CompactClientFaultBody(body []byte) []byte {
	if len(body) <= MaxClientFaultBodyBytes || !bytes.Contains(body, []byte(`"error"`)) {
		return body
	}
	details, isError := terminalErrorFromLines([]string{DataPrefix + string(body)})
	if !isError || !IsClientFaultError(details) || chunkCarriesOutput(body) {
		return body
	}
	code, err := strconv.Atoi(strings.TrimSpace(details.Code))
	if err != nil {
		return body
	}
	compact, err := json.Marshal(compactClientFault{Error: compactClientFaultError{
		Code:    code,
		Message: truncateUTF8(details.Message, maxClientFaultMessageBytes),
		Type:    truncateUTF8(details.Type, maxClientFaultTypeBytes),
	}})
	if err != nil {
		return body
	}
	return compact
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

func IsUnbilledClientFault(responsePayload []byte, inputTokens, outputTokens uint64) bool {
	return inputTokens == 0 && outputTokens == 0 && IsClientFaultErrorResponse(responsePayload)
}

type outputMessage struct {
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content"`
	Reasoning        string            `json:"reasoning"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
	Refusal          string            `json:"refusal"`
	FunctionCall     *json.RawMessage  `json:"function_call"`
}

func (m *outputMessage) carriesOutput() bool {
	return m != nil && (m.Content != "" || m.ReasoningContent != "" || m.Reasoning != "" || len(m.ToolCalls) > 0 || m.Refusal != "" || m.FunctionCall != nil)
}

func StreamedLineCarriesOutput(line string) bool {
	payload, isData := sseDataJSON(line)
	return isData && chunkCarriesOutput(payload)
}

func chunkCarriesOutput(payload []byte) bool {
	var chunk struct {
		Choices []struct {
			Delta    *outputMessage `json:"delta"`
			Message  *outputMessage `json:"message"`
			Text     string         `json:"text"`
			Logprobs *struct {
				Content []json.RawMessage `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
		Usage *struct {
			CompletionTokens uint64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return true
	}
	if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
		return true
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.carriesOutput() || choice.Message.carriesOutput() || choice.Text != "" {
			return true
		}
		if choice.Logprobs != nil && len(choice.Logprobs.Content) > 0 {
			return true
		}
	}
	return false
}

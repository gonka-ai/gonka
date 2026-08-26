package completionapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ErrorDetails is the OpenAI-style top-level error extracted from a stored
// response payload. Fields are empty when no such object is present.
type ErrorDetails struct {
	Type    string
	Code    string
	Message string
}

// IsTerminalErrorResponse reports whether a stored response payload is an
// error envelope carrying no usable completion.
//
// This is the consensus rule for TIMEOUT_REASON_ERROR. Every host must compile
// against this one implementation. There is no independent backstop: verifiers
// run the same predicate over the same hash-pinned bytes as the gateway, so a
// misclassification is agreed, not caught. Ambiguous cases therefore return
// ok == false, which degrades to today's behaviour (no miss).
//
// Accept only when all hold, parsed via NewCompletionResponseFromLinesFromResponsePayload:
//   - some event carries a top-level error object (the {"error":{code,message,type}}
//     shape, matching the gateway's sseChunkErrorPayload);
//   - no content anywhere — no delta.content, delta.reasoning_content,
//     delta.tool_calls, message.content, choice.text (and, fail-closed, the
//     sibling reasoning / message.tool_calls fields);
//   - no unparseable extra `data:` JSON (fail-closed: when in doubt, no miss);
//   - usage.completion_tokens == 0. When no event carries usage, GetUsage
//     synthesizes CompletionTokens = len(logprobs.Content), so this mostly
//     restates "no content anywhere". It still catches a host that attaches a
//     real usage block to an error envelope.
func IsTerminalErrorResponse(responsePayload []byte) (details ErrorDetails, ok bool) {
	if len(responsePayload) == 0 {
		return ErrorDetails{}, false
	}
	resp, err := NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		return ErrorDetails{}, false
	}
	streamed, isStreamed := resp.(*StreamedCompletionResponse)
	if !isStreamed {
		// JSON (non-streamed) errors are out of scope: their bodies typically
		// carry no usage block, GetUsage fails, and no Finish is ever signed.
		return ErrorDetails{}, false
	}
	details, hasError := terminalErrorFromLines(streamed.Lines)
	if !hasError {
		return ErrorDetails{}, false
	}
	if streamedHasUsableContent(streamed) || streamedHasUnparseableData(streamed) {
		return details, false
	}
	usage, err := streamed.GetUsage()
	if err != nil {
		return details, false
	}
	if usage.CompletionTokens != 0 {
		return details, false
	}
	return details, true
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

func streamedHasUsableContent(streamed *StreamedCompletionResponse) bool {
	if streamed == nil {
		return false
	}
	for _, line := range streamed.Lines {
		if sseLineHasUsableContent(line) {
			return true
		}
	}
	for _, event := range streamed.Resp.Data {
		for _, choice := range event.Choices {
			if choice.Text != "" {
				return true
			}
			if choice.Message != nil && choice.Message.Content != "" {
				return true
			}
			if choice.Delta != nil && choice.Delta.Content != nil && *choice.Delta.Content != "" {
				return true
			}
		}
	}
	return false
}

func sseLineHasUsableContent(line string) bool {
	payload, ok := sseDataJSON(line)
	if !ok {
		return false
	}
	var evt struct {
		Choices []struct {
			Text  string `json:"text"`
			Delta *struct {
				Content          string          `json:"content"`
				Reasoning        string          `json:"reasoning"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			Message *struct {
				Content          string          `json:"content"`
				Reasoning        string          `json:"reasoning"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		return false
	}
	for _, c := range evt.Choices {
		if c.Text != "" {
			return true
		}
		if c.Delta != nil {
			if c.Delta.Content != "" || c.Delta.Reasoning != "" || c.Delta.ReasoningContent != "" {
				return true
			}
			if jsonArrayHasElements(c.Delta.ToolCalls) {
				return true
			}
		}
		if c.Message != nil {
			if c.Message.Content != "" || c.Message.Reasoning != "" || c.Message.ReasoningContent != "" {
				return true
			}
			if jsonArrayHasElements(c.Message.ToolCalls) {
				return true
			}
		}
	}
	return false
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

func streamedHasUnparseableData(streamed *StreamedCompletionResponse) bool {
	if streamed == nil {
		return false
	}
	for _, line := range streamed.Lines {
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

func jsonArrayHasElements(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' {
		return false
	}
	inner := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	return len(inner) > 0
}

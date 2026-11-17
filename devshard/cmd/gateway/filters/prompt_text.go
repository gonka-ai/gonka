package filters

import (
	"bytes"

	json "github.com/goccy/go-json"
)

type promptBody struct {
	Messages []promptMessage `json:"messages"`
	Tools    promptField     `json:"tools"`
}

type promptMessage struct {
	Content      promptField  `json:"content"`
	ToolCalls    []promptCall `json:"tool_calls"`
	FunctionCall *promptCall  `json:"function_call"`
}

type promptCall struct {
	Function  *promptCall `json:"function"`
	Name      promptField `json:"name"`
	Arguments promptField `json:"arguments"`
}

type promptField string

const noPromptText promptField = ""

func (f *promptField) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, jsonNull) {
		return nil
	}
	if bytes.HasPrefix(data, jsonStringStart) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*f = promptField(text)
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var rendered bytes.Buffer
	encoder := json.NewEncoder(&rendered)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	*f = promptField(bytes.TrimSuffix(rendered.Bytes(), jsonLineEnd))
	return nil
}

// PromptText returns the text of a normalized body a host's chat template renders: message contents, tool calls and tool definitions, without the template itself. See README.md, "The prompt text".
func PromptText(body []byte) ([]string, error) {
	var decoded promptBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(decoded.Messages)+1)
	add := func(field promptField) {
		if field != noPromptText {
			parts = append(parts, string(field))
		}
	}
	addCall := func(call *promptCall) {
		if call.Function != nil {
			call = call.Function
		}
		add(call.Name)
		add(call.Arguments)
	}
	for messageIndex := range decoded.Messages {
		message := &decoded.Messages[messageIndex]
		add(message.Content)
		for callIndex := range message.ToolCalls {
			addCall(&message.ToolCalls[callIndex])
		}
		if message.FunctionCall != nil {
			addCall(message.FunctionCall)
		}
	}
	add(decoded.Tools)
	return parts, nil
}

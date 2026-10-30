package mockopenai

import (
	"encoding/json"
	"net/http"

	"devshard/testenv/replay"

	"github.com/labstack/echo/v4"
)

// handleRender stands in for a model's render server (`vllm launch render`)
// on recorded samples. It counts with the numbers the build's render server
// gave for the recording: the prompt for a request that ends with a
// generation prompt, and for one that ends with an assistant turn the prompt
// plus that turn's count. A turn of other text than the recording served is
// counted in proportion to its bytes. A request without a recording is 404,
// as a render server that does not serve the model answers.
//
// A voter names the thinking mode of an answer that carried reasoning. A
// build renders a mode named the way it defaults to as it renders the
// request that does not name it, so a recording of the request without its
// template options whose answer reasoned counts the turn it served, and the
// prompt when no recording names the mode.
func (s *Server) handleRender(c echo.Context) error {
	body, err := readBody(c.Request())
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "mock-openai render: " + err.Error()})
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "mock-openai render: body is not a JSON object"})
	}
	messages, _ := request["messages"].([]any)
	var turn map[string]any
	if request["add_generation_prompt"] == false && len(messages) > 0 {
		if last, ok := messages[len(messages)-1].(map[string]any); ok && last["role"] == "assistant" {
			turn = last
			request["messages"] = messages[:len(messages)-1]
			delete(request, "add_generation_prompt")
			if body, err = json.Marshal(request); err != nil {
				return err
			}
		}
	}
	var counts *replay.RenderCounts
	var bytes uint64
	if turn != nil {
		bytes = turnTextBytes(turn)
	}
	// Recordings that differ only in their output limits share the prompt;
	// the turn picks the one that served it.
	candidates := s.replay.LookupRender(body)
	if _, named := request["chat_template_kwargs"]; named && !servesTurn(candidates, turn != nil, bytes) {
		delete(request, "chat_template_kwargs")
		unnamed, err := json.Marshal(request)
		if err != nil {
			return err
		}
		for _, sample := range s.replay.LookupRender(unnamed) {
			if reasoned(sample) && (len(candidates) == 0 || servesTurn([]replay.Sample{sample}, turn != nil, bytes)) {
				candidates = append(candidates, sample)
			}
		}
	}
	for _, sample := range candidates {
		if sample.Render != nil && (counts == nil || sample.Render.TurnBytes == bytes) {
			counts = sample.Render
		}
	}
	if counts == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "mock-openai render: no recording for the request"})
	}
	n := counts.Input
	if turn != nil {
		if bytes == counts.TurnBytes {
			n += counts.Output
		} else if counts.TurnBytes > 0 {
			n += counts.Output * bytes / counts.TurnBytes
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"token_ids": make([]int, n)})
}

// servesTurn reports a recording among samples that served a turn of bytes,
// or, for a prompt without a turn, any recording.
func servesTurn(samples []replay.Sample, withTurn bool, bytes uint64) bool {
	for _, sample := range samples {
		if sample.Render != nil && (!withTurn || sample.Render.TurnBytes == bytes) {
			return true
		}
	}
	return false
}

// reasoned reports a recording whose answer carried reasoning: its build
// generated it in thinking mode.
func reasoned(sample replay.Sample) bool {
	if sample.Recorded == nil {
		return false
	}
	for _, chunk := range sample.Recorded.Stream {
		var event struct {
			Choices []struct {
				Delta struct {
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(chunk, &event) != nil {
			continue
		}
		for _, choice := range event.Choices {
			if choice.Delta.Reasoning != "" || choice.Delta.ReasoningContent != "" {
				return true
			}
		}
	}
	return false
}

// turnTextBytes counts the text of an assistant turn the way the recording's
// turn bytes were counted: content, reasoning, and each call's name and
// arguments.
func turnTextBytes(turn map[string]any) uint64 {
	n := 0
	for _, field := range []string{"content", "reasoning", "reasoning_content"} {
		if text, ok := turn[field].(string); ok {
			n += len(text)
		}
	}
	calls, _ := turn["tool_calls"].([]any)
	for _, raw := range calls {
		call, _ := raw.(map[string]any)
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)
		n += len(name) + len(arguments)
	}
	return uint64(n)
}

package mockopenai_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/host"
	"devshard/testenv/mockopenai"
	"devshard/testenv/replay"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// servedTurn rebuilds the assistant turn a recording streamed: content,
// reasoning and each tool call with its streamed arguments.
func servedTurn(t *testing.T, sample replay.Sample) map[string]any {
	t.Helper()
	content, reasoning := "", ""
	type call struct{ id, name, arguments string }
	var calls []*call
	byIndex := map[int]*call{}
	for _, chunk := range sample.Recorded.Stream {
		var event struct {
			Choices []struct {
				Delta struct {
					Content          *string `json:"content"`
					Reasoning        *string `json:"reasoning"`
					ReasoningContent *string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, json.Unmarshal(chunk, &event))
		for _, choice := range event.Choices {
			delta := choice.Delta
			if delta.Content != nil {
				content += *delta.Content
			}
			if delta.Reasoning != nil {
				reasoning += *delta.Reasoning
			} else if delta.ReasoningContent != nil {
				reasoning += *delta.ReasoningContent
			}
			for _, tc := range delta.ToolCalls {
				c, ok := byIndex[tc.Index]
				if !ok {
					c = &call{}
					byIndex[tc.Index] = c
					calls = append(calls, c)
				}
				if tc.ID != "" {
					c.id = tc.ID
				}
				c.name += tc.Function.Name
				c.arguments += tc.Function.Arguments
			}
		}
	}
	turn := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		turn["reasoning"] = reasoning
	}
	if len(calls) > 0 {
		list := make([]any, 0, len(calls))
		for _, c := range calls {
			list = append(list, map[string]any{"id": c.id, "type": "function", "function": map[string]any{"name": c.name, "arguments": c.arguments}})
		}
		turn["tool_calls"] = list
	}
	return turn
}

// Test flow:
//  1. Count every recording's request and served turn through the host's render client against the mock.
//  2. Require the build's own render counts back, and a turn of other text to count in proportion to its bytes.
func TestRenderAnswersWithTheRecordedCounts(t *testing.T) {
	for _, file := range recordedFiles(t) {
		srv := newRecordedServer(t, file, mockopenai.FaultConfig{})
		samples := loadRecordedSamples(t, file)
		shared := map[string]int{}
		for _, sample := range samples {
			fingerprint, err := replay.RenderFingerprint(sample.Request)
			require.NoError(t, err)
			shared[fingerprint]++
		}
		for _, sample := range samples {
			t.Run(file+"/"+sample.Model+"/"+sample.Name, func(t *testing.T) {
				fingerprint, err := replay.RenderFingerprint(sample.Request)
				require.NoError(t, err)
				requireRecordedCounts(t, srv, sample, shared[fingerprint] == 1)
			})
		}
	}
}

// requireRecordedCounts counts sample's request and turns on srv. unique
// marks a request no other recording shares once its output limits are left
// out, so a turn of other text can only be counted against this recording.
func requireRecordedCounts(t *testing.T, srv *httptest.Server, sample replay.Sample, unique bool) {
	t.Helper()
	require.NotNil(t, sample.Render)
	client := host.NewRenderClient(map[string][]string{sample.Model: {srv.URL}}, nil)
	ctx := context.Background()
	input, _, err := client.CountPrompt(ctx, sample.Model, sample.Request)
	require.NoError(t, err)
	require.Equal(t, sample.Render.Input, input)

	output, _, err := client.CountTurn(ctx, sample.Model, sample.Request, servedTurn(t, sample))
	require.NoError(t, err)
	require.Equal(t, sample.Render.Output, output)

	empty, _, err := client.CountTurn(ctx, sample.Model, sample.Request, map[string]any{"role": "assistant", "content": ""})
	require.NoError(t, err)
	require.Zero(t, empty, "an empty turn counts nothing")

	// A voter names the thinking mode of an answer that carried reasoning;
	// a build counts it as the request that named none.
	turn := servedTurn(t, sample)
	allowance, _ := types.UsageAllowanceFor(sample.Model)
	var body map[string]any
	require.NoError(t, json.Unmarshal(sample.Request, &body))
	if _, named := body["chat_template_kwargs"]; !named && allowance.ThinkingKwarg != "" && turn["reasoning"] != nil {
		body["chat_template_kwargs"] = map[string]any{allowance.ThinkingKwarg: true}
		request, err := json.Marshal(body)
		require.NoError(t, err)
		input, _, err := client.CountPrompt(ctx, sample.Model, request)
		require.NoError(t, err)
		require.Equal(t, sample.Render.Input, input)
		output, _, err := client.CountTurn(ctx, sample.Model, request, turn)
		require.NoError(t, err)
		require.Equal(t, sample.Render.Output, output)
	}

	half := sample.Render.TurnBytes / 2
	other, _, err := client.CountTurn(ctx, sample.Model, sample.Request, map[string]any{"role": "assistant", "content": strings.Repeat("x", int(half))})
	require.NoError(t, err)
	if unique && half != sample.Render.TurnBytes {
		require.Equal(t, sample.Render.Output*half/sample.Render.TurnBytes, other)
	}
}

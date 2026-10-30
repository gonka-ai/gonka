package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"devshard/testenv/replay"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Stream every recording of every ML node build through the gateway's chunk scan, as the relay does.
//  2. Require the honest claim to stay within the byte bounds the served binding puts on it, output and input.
//  3. The one exception is a client logit_bias on single-byte fragments, which decode to no text: the gateway
//     rejects the request, so it never reaches an executor.
func TestRecordedHonestClaimsStayWithinTheByteBounds(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	files, err := filepath.Glob("../../testenv/mockopenai/testdata/vllm_recorded_*.jsonl.gz")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	checked := 0
	for _, file := range files {
		dataset, err := replay.LoadFile(file)
		require.NoError(t, err)
		build := strings.TrimSuffix(filepath.Base(file), ".jsonl.gz")
		for i := 0; i < dataset.Len(); i++ {
			sample, ok := dataset.Sample(i)
			require.True(t, ok)
			t.Run(build+"/"+sample.Model+"/"+sample.Name, func(t *testing.T) {
				forwarded, _, err := upstreamChatRequestPipeline().Normalize(sample.ClientRequest, false, defaultOutputTokenLimits(), sample.Model)
				if biasedFragments(t, sample.Request) {
					require.ErrorContains(t, err, "logit_bias is not supported", "the gateway rejects the forcing request")
					return
				}
				require.NoError(t, err)
				var scanned struct{ TextBytes, ToolCalls, Arguments uint64 }
				var calls toolCallSet
				var usage struct {
					PromptTokens     uint64 `json:"prompt_tokens"`
					CompletionTokens uint64 `json:"completion_tokens"`
				}
				for _, chunk := range sample.Recorded.Stream {
					scan := scanSSEChunk(append(append([]byte("data: "), chunk...), '\n'))
					scanned.TextBytes += scan.TextBytes
					scanned.ToolCalls = calls.add(scan.ToolCallIndexes)
					scanned.Arguments = calls.addArguments(scan.ToolCallArguments)
					var event struct {
						Usage *json.RawMessage `json:"usage"`
					}
					require.NoError(t, json.Unmarshal(chunk, &event))
					if event.Usage != nil && string(*event.Usage) != "null" {
						require.NoError(t, json.Unmarshal(*event.Usage, &usage))
					}
				}
				terms := requestTextTerms(sample.Request)
				stopBytes := terms.stopBytes
				require.True(t, terms.textOnly)
				require.True(t, requestTextTerms(forwarded).streamed, "the gateway asks the ML node to stream")
				outputBound := types.OutputByteBound(sample.Model, scanned.TextBytes, stopBytes, scanned.ToolCalls, scanned.Arguments, uint64(sample.MaxTokens))
				inputBound := types.InputByteBound(sample.Model, uint64(len(forwarded)))
				require.NotZero(t, inputBound, "the model has an allowance")
				require.LessOrEqual(t, usage.PromptTokens, inputBound, "input")
				require.LessOrEqual(t, usage.CompletionTokens, outputBound,
					"output: %d text bytes, %d tool calls, stop %d bytes", scanned.TextBytes, scanned.ToolCalls, stopBytes)
				checked++
			})
		}
	}
	require.Greater(t, checked, 300)
}

// biasedFragments reports a request with a positive logit_bias, which the forced_fragment
// recordings put on a token that is one byte of a multi-byte character.
func biasedFragments(t *testing.T, request []byte) bool {
	t.Helper()
	var body struct {
		LogitBias map[string]float64 `json:"logit_bias"`
	}
	require.NoError(t, json.Unmarshal(request, &body))
	for _, bias := range body.LogitBias {
		if bias > 0 {
			return true
		}
	}
	return false
}

// Test flow:
//  1. Scan deltas carrying content, reasoning under both keys, and a tool call split over two deltas.
//  2. Require the text bytes to count reasoning once, the tool call once, and the second choice not at all.
func TestScanSSEChunkCountsTheFirstChoiceText(t *testing.T) {
	scan := scanSSEChunk([]byte(`data: {"choices":[{"index":0,"delta":{"content":"héllo","reasoning":"why","reasoning_content":"why"}},{"index":1,"delta":{"content":"other choice"}}]}` + "\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}` + "\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\": \"Paris\"}"}}]}}]}` + "\n"))
	require.Equal(t, uint64(len("héllo")+len("why")+len("get_weather")+len(`{"city": "Paris"}`)), scan.TextBytes)
	var calls toolCallSet
	require.Equal(t, uint64(1), calls.add(scan.ToolCallIndexes))

	// A whole message next to a delta is not text a streaming client reads, so it neither counts
	// nor turns the bound off.
	message := scanSSEChunk([]byte(`data: {"choices":[{"index":0,"delta":{"content":"ab"},"message":{"content":"whole answer"}}]}` + "\n"))
	require.Equal(t, uint64(2), message.TextBytes)
}

// Test flow:
//  1. Count the calls of an answer whose one call has its name spread over three deltas, then a second call.
//  2. Require one call per index, as a client assembles them, whatever number of deltas name it.
func TestToolCallsCountOncePerIndex(t *testing.T) {
	var calls toolCallSet
	var counted uint64
	for _, chunk := range []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"wea"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"ther","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"get_time","arguments":"{}"}}]}}]}`,
	} {
		counted = calls.add(scanSSEChunk([]byte("data: " + chunk + "\n")).ToolCallIndexes)
	}
	require.Equal(t, uint64(2), counted)
}

// Test flow:
//  1. Stream two calls' arguments in fragments that split keys, strings and nested values.
//  2. Require one count per top-level key: a colon inside a string or a nested object is not one.
func TestToolCallArgumentsCountTheTopLevelKeys(t *testing.T) {
	var calls toolCallSet
	var counted uint64
	for _, chunk := range []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{\"a\":1,\"b"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":\"x:\\\"y\",\"c\":{\"d\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"[1,{\"e\":2}]}}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"g","arguments":"{\"z\":true}"}}]}}]}`,
	} {
		counted = calls.addArguments(scanSSEChunk([]byte("data: " + chunk + "\n")).ToolCallArguments)
	}
	require.Equal(t, uint64(4), counted, "a, b, c of the first call and z of the second")
}

// Test flow:
//  1. Take DeepSeek-V4 calls of 1 to 40 short integer arguments with one-letter keys, the shape
//     whose markup outgrows the arguments' bytes most, and the turn counts measured on the render
//     servers of the 3.0.16, 3.1.0 and 4.0.0 ML node builds (the same on all three).
//  2. Require each honest claim, its rendered turn, within the output byte bound.
func TestManyShortArgumentsStayWithinTheByteBound(t *testing.T) {
	for _, call := range []struct {
		arguments, bytes, rendered uint64
	}{
		{1, 8, 42}, {3, 20, 74}, {10, 62, 186}, {20, 122, 346}, {40, 256, 666},
	} {
		bound := types.OutputByteBound(types.DeepSeekV4FlashModelID, call.bytes, 0, 1, call.arguments, 100000)
		require.LessOrEqual(t, call.rendered, bound, "%d arguments", call.arguments)
	}
}

func TestRequestTextTerms(t *testing.T) {
	terms := requestTextTerms([]byte(`{"stream":true,"stop":["ab","abcdé"],"messages":[{"role":"user","content":"x"},{"role":"assistant","content":null},{"role":"user","content":[{"type":"text","text":"y"}]}]}`))
	require.Equal(t, textTerms{stopBytes: uint64(len("abcdé")), textOnly: true, streamed: true}, terms)
	terms = requestTextTerms([]byte(`{"stop":"single","messages":[]}`))
	require.Equal(t, textTerms{stopBytes: 6, textOnly: true}, terms, "a body without stream is answered as a message")
	require.False(t, requestTextTerms([]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`)).textOnly)
	require.Equal(t, textTerms{}, requestTextTerms([]byte(`not json`)))
}

package completionapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessingJsonResponse(t *testing.T) {
	processor := NewExecutorResponseProcessor("dummy-id", true)
	processor.ProcessJSONResponse([]byte("dummy-response"))
}

const EVENT = `

data: {"id":"cmpl-3973dab1430143849df83d943ea0c7ac","object":"chat.completion.chunk","created":1726472629,"model":"Qwen/Qwen2.5-7B-Instruct","choices":[{"index":0,"delta":{"content":"9"},"logprobs":{"content":[{"token":"9","logprob":0.0,"bytes":[57],"top_logprobs":[{"token":"9","logprob":0.0,"bytes":[57]},{"token":"8","logprob":-23.125,"bytes":[56]},{"token":"0","logprob":-24.125,"bytes":[48]}]}]},"finish_reason":null}]}
`

// INCOMPRESSIBLE names a position no alternative explains, so the stored copy falls back to the whole one.
const INCOMPRESSIBLE = `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"7"},"logprobs":{"content":[{"token":"7","logprob":0.0,"bytes":[55],"top_logprobs":[{"token":"9","logprob":0.0,"bytes":[57]},{"token":"8","logprob":-23.125,"bytes":[56]}]}]},"finish_reason":null}]}`

func TestProcessingStreamedEvents(t *testing.T) {
	dummyID := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyID, true)
	var updatedLine string
	var err error
	updatedLine, err = processor.ProcessStreamedResponse(strings.TrimSpace(EVENT))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	println(updatedLine)

	if !strings.Contains(updatedLine, dummyID) {
		t.Fatalf("expected %s to contain %s", updatedLine, dummyID)
	}

	bytes, err := processor.GetResponseBytes()
	if err != nil {
		t.Fatalf("unexpected error for GetResponseBytes: %v", err)
	}

	println(string(bytes))
}

func TestCompletionTokenCountForStreamedResponse(t *testing.T) {
	dummyID := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyID, true)

	events := readLines(t, "test_data/response_streamed.txt")
	require.NotEmpty(t, events, "Read 0 events from responseprocessor_test_data.txt")
	for _, event := range events {
		_, err := processor.ProcessStreamedResponse(event)
		require.NoError(t, err, "failed to process a line of a streamed response")
	}

	response, err := processor.GetResponse()
	fmt.Printf("Response: %+v\n", response)
	require.NoError(t, err, "GetResponse failed")
	id, err := response.GetInferenceId()
	require.NoError(t, err)
	require.Equal(t, dummyID, id, "expected inference id to be %s, got %s", dummyID, id)
	model, err := response.GetModel()
	require.NoError(t, err)
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)
	usage, err := response.GetUsage()
	require.NoError(t, err)
	expectedUsage := &Usage{
		PromptTokens:     31,
		CompletionTokens: 10,
	}
	require.NotNil(t, usage, "expected usage to be not nil")
	require.Equal(t, *expectedUsage, *usage, "expected usage to be %v, got %v", *expectedUsage, *usage)

	hash, err := response.GetHash()
	require.NoError(t, err, "GetHash failed")
	require.NotEmpty(t, hash, "expected hash to be not empty")
}

func TestCompletionTokenCountForStreamedResponseWithTokenIds(t *testing.T) {
	dummyID := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyID, true)

	events := readLines(t, "test_data/response_streamed_token_ids.txt")
	require.NotEmpty(t, events, "Read 0 events from responseprocessor_test_data.txt")
	for _, event := range events {
		_, err := processor.ProcessStreamedResponse(event)
		require.NoError(t, err, "failed to process a line of a streamed response")
	}

	response, err := processor.GetResponse()
	require.NoError(t, err)

	enforcedTokens, err := response.GetEnforcedTokens()
	require.NoError(t, err, "GetEnforcedTokens failed")
	require.NotEmpty(t, enforcedTokens, "expected enforced tokens to be not empty")
	require.Len(t, enforcedTokens.Tokens, 44, "expected 1 enforced token")

	require.NoError(t, err, "GetResponse failed")
	model, err := response.GetModel()
	require.NoError(t, err)
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)

	hash, err := response.GetHash()
	require.NoError(t, err, "GetHash failed")
	require.NotEmpty(t, hash, "expected hash to be not empty")
}

func TestCompletionTokenCountForWholeResponseWithTokenIds(t *testing.T) {
	dummyID := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyID, true)

	responseBytes, err := loadJSON("test_data/response_token_ids.json")
	require.NoError(t, err, "failed to load json response")

	_, err = processor.ProcessJSONResponse(responseBytes)
	require.NoError(t, err, "failed to process json response")

	response, err := processor.GetResponse()
	require.NoError(t, err, "GetResponse failed")
	model, err := response.GetModel()
	require.NoError(t, err, "GetModel failed")
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)
	usage, err := response.GetUsage()
	require.NoError(t, err, "GetUsage failed")
	require.NotNil(t, usage, "expected usage to be not nil")

	hash, err := response.GetHash()
	require.NoError(t, err, "GetHash failed")
	require.NotEmpty(t, hash, "expected hash to be not empty")

	enforcedTokens, err := response.GetEnforcedTokens()
	require.NoError(t, err, "GetEnforcedTokens failed")
	require.NotEmpty(t, enforcedTokens, "expected enforced tokens to be not empty")
	require.Len(t, enforcedTokens.Tokens, 28, "expected 28 enforced tokens")
}

func readLines(t *testing.T, name string) []string {
	t.Helper()

	f, err := os.Open(name)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return lines
}

func TestCompletionTokenCountForWholeResponse(t *testing.T) {
	dummyID := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyID, true)

	responseBytes, err := loadJSON("test_data/response.json")
	require.NoError(t, err, "failed to load json response")

	_, err = processor.ProcessJSONResponse(responseBytes)
	require.NoError(t, err, "failed to process json response")

	response, err := processor.GetResponse()
	require.NoError(t, err, "GetResponse failed")
	id, err := response.GetInferenceId()
	require.NoError(t, err)
	require.Equal(t, dummyID, id, "expected inference id to be %s, got %s", dummyID, id)
	model, err := response.GetModel()
	require.NoError(t, err)
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)
	usage, err := response.GetUsage()
	require.NoError(t, err)
	expectedUsage := &Usage{
		PromptTokens:     31,
		CompletionTokens: 10,
	}
	require.NotNil(t, usage, "expected usage to be not nil")
	require.Equal(t, *expectedUsage, *usage, "expected usage to be %v, got %v", *expectedUsage, *usage)

	hash, err := response.GetHash()
	require.NoError(t, err, "GetHash failed")
	require.NotEmpty(t, hash, "expected hash to be not empty")
}

func loadJSON(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return data, nil
}

// The executor always runs and stores with logprobs; only the copy sent back to the caller changes.
func TestForwardingLogprobsOnlyWhenAsked(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		event           string
		forwardLogprobs bool
		storedWhole     bool
		wantContent     string
	}{
		{name: "caller asked", event: EVENT, forwardLogprobs: true, wantContent: `"content":"9"`},
		{name: "caller did not ask", event: EVENT, wantContent: `"content":"9"`},
		{name: "caller asked, chunk will not compress", event: INCOMPRESSIBLE, forwardLogprobs: true, storedWhole: true, wantContent: `"content":"7"`},
		{name: "caller did not ask, chunk will not compress", event: INCOMPRESSIBLE, storedWhole: true, wantContent: `"content":"7"`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			processor := NewExecutorResponseProcessor("dummy-id", testCase.forwardLogprobs)

			forwarded, err := processor.ProcessStreamedResponse(strings.TrimSpace(testCase.event))
			require.NoError(t, err)
			require.Contains(t, forwarded, testCase.wantContent, "the answer itself survives either way: %s", forwarded)
			carried, whole := forwardedLogprobs(t, forwarded)
			require.Equal(t, testCase.forwardLogprobs, carried, "forwarded copy: %s", forwarded)
			require.Equal(t, testCase.forwardLogprobs, whole,
				"an asking caller gets the host's own positions, not the compressed ones: %s", forwarded)

			stored, err := processor.GetResponseBytes()
			require.NoError(t, err)
			position := storedPosition(t, stored)
			require.NotEmpty(t, position["top_logprobs"],
				"the stored copy always keeps the alternatives the validator replays against: %s", stored)
			_, keptWhole := position["logprob"]
			require.Equal(t, testCase.storedWhole, keptWhole,
				"a chunk that compresses loses the position logprob an alternative already spells; one that does not is stored whole: %s", stored)
		})
	}
}

// forwardedLogprobs reports whether the caller was sent logprobs, and whether they are the uncompressed ones.
func forwardedLogprobs(t *testing.T, forwarded string) (carried, whole bool) {
	t.Helper()
	position, carried := firstLogprobPosition(t, []byte(strings.TrimPrefix(forwarded, DataPrefix)))
	if !carried {
		return false, false
	}
	_, whole = position["logprob"]
	return true, whole
}

// storedPosition digs the first position out of a stored envelope, whose events are JSON strings.
func storedPosition(t *testing.T, stored []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Events []string `json:"events"`
	}
	require.NoError(t, json.Unmarshal(stored, &envelope))
	require.NotEmpty(t, envelope.Events, "stored envelope carries no events: %s", stored)

	position, carried := firstLogprobPosition(t, []byte(strings.TrimPrefix(envelope.Events[0], DataPrefix)))
	require.True(t, carried, "the stored copy always keeps the logprobs the validator replays against: %s", stored)
	return position
}

// firstLogprobPosition digs the first position out of one chunk, reporting whether it carried any.
func firstLogprobPosition(t *testing.T, chunk []byte) (map[string]any, bool) {
	t.Helper()
	var payload struct {
		Choices []struct {
			Logprobs struct {
				Content []map[string]any `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(chunk, &payload))
	require.NotEmpty(t, payload.Choices, "chunk carries no choices: %s", chunk)
	if len(payload.Choices[0].Logprobs.Content) == 0 {
		return nil, false
	}
	return payload.Choices[0].Logprobs.Content[0], true
}

// The executor runs this on every chunk, so a copy nobody reads is the most expensive line here.
func BenchmarkPrepareBody(b *testing.B) {
	chunk := []byte(strings.TrimPrefix(strings.TrimSpace(EVENT), DataPrefix))
	for _, testCase := range []struct {
		name string
		ask  bool
	}{{name: "caller did not ask"}, {name: "caller asked", ask: true}} {
		b.Run(testCase.name, func(b *testing.B) {
			processor := NewExecutorResponseProcessor("bench", testCase.ask)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, _, err := processor.prepareBody(chunk); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

package completionapi

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessingJsonResponse(t *testing.T) {
	processor := NewExecutorResponseProcessor("dummy-id")
	processor.ProcessJsonResponse([]byte("dummy-response"))
}

const EVENT = `
data: {"id":"cmpl-3973dab1430143849df83d943ea0c7ac","object":"chat.completion.chunk","created":1726472629,"model":"Qwen/Qwen2.5-7B-Instruct","choices":[{"index":0,"delta":{"content":"9"},"logprobs":{"content":[{"token":"9","logprob":0.0,"bytes":[57],"top_logprobs":[{"token":"9","logprob":0.0,"bytes":[57]},{"token":"8","logprob":-23.125,"bytes":[56]},{"token":"0","logprob":-24.125,"bytes":[48]}]}]},"finish_reason":null}]}
`

func TestProcessingStreamedEvents(t *testing.T) {
	dummyId := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyId)
	var updatedLine string
	var err error
	updatedLine, err = processor.ProcessStreamedResponse(strings.TrimSpace(EVENT))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	println(updatedLine)

	if !strings.Contains(updatedLine, dummyId) {
		t.Fatalf("expected %s to contain %s", updatedLine, dummyId)
	}

	bytes, err := processor.GetResponseBytes()
	if err != nil {
		t.Fatalf("unexpected error for GetResponseBytes: %v", err)
	}

	println(string(bytes))
}

func TestCompletionTokenCountForStreamedResponse(t *testing.T) {
	dummyId := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyId)

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
	require.Equal(t, dummyId, id, "expected inference id to be %s, got %s", dummyId, id)
	model, err := response.GetModel()
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)
	usage, err := response.GetUsage()
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

func TestCompletionTokenCountForPartialStreamUsesLogprobEntries(t *testing.T) {
	processor := NewExecutorResponseProcessor("partial-id")
	events := []string{
		`data: {"id":"upstream","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"content":"ab"},"logprobs":{"content":[{"token":"a","logprob":-0.1,"top_logprobs":[{"token":"a","logprob":-0.1},{"token":"x","logprob":-1.1},{"token":"y","logprob":-2.1}]},{"token":"b","logprob":-0.2,"top_logprobs":[{"token":"b","logprob":-0.2},{"token":"m","logprob":-1.2},{"token":"n","logprob":-2.2}]}]},"finish_reason":null}]}`,
		`data: {"id":"upstream","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"content":"c"},"logprobs":{"content":[{"token":"c","logprob":-0.3,"top_logprobs":[{"token":"c","logprob":-0.3},{"token":"q","logprob":-1.3},{"token":"r","logprob":-2.3},{"token":"s","logprob":-3.3}]}]},"finish_reason":null}]}`,
	}
	for _, event := range events {
		_, err := processor.ProcessStreamedResponse(event)
		require.NoError(t, err)
	}

	response, err := processor.GetResponse()
	require.NoError(t, err)
	usage, err := response.GetUsage()
	require.NoError(t, err)
	require.Equal(t, uint64(0), usage.PromptTokens)
	require.Equal(t, uint64(3), usage.CompletionTokens)
}

func TestCompletionTokenCountForStreamedResponseWithTokenIds(t *testing.T) {
	dummyId := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyId)

	events := readLines(t, "test_data/response_streamed_token_ids.txt")
	require.NotEmpty(t, events, "Read 0 events from responseprocessor_test_data.txt")
	for _, event := range events {
		_, err := processor.ProcessStreamedResponse(event)
		require.NoError(t, err, "failed to process a line of a streamed response")
	}

	response, err := processor.GetResponse()

	enforcedTokens, err := response.GetEnforcedTokens()
	require.NoError(t, err, "GetEnforcedTokens failed")
	require.NotEmpty(t, enforcedTokens, "expected enforced tokens to be not empty")
	require.Equal(t, 44, len(enforcedTokens.Tokens), "expected 1 enforced token")

	require.NoError(t, err, "GetResponse failed")
	model, err := response.GetModel()
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)

	hash, err := response.GetHash()
	require.NoError(t, err, "GetHash failed")
	require.NotEmpty(t, hash, "expected hash to be not empty")
}

func TestCompletionTokenCountForWholeResponseWithTokenIds(t *testing.T) {
	dummyId := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyId)

	responseBytes, err := loadJson("test_data/response_token_ids.json")
	require.NoError(t, err, "failed to load json response")

	_, err = processor.ProcessJsonResponse(responseBytes)
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
	require.Equal(t, 28, len(enforcedTokens.Tokens), "expected 28 enforced tokens")
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
	dummyId := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyId)

	responseBytes, err := loadJson("test_data/response.json")
	require.NoError(t, err, "failed to load json response")

	_, err = processor.ProcessJsonResponse(responseBytes)
	require.NoError(t, err, "failed to process json response")

	response, err := processor.GetResponse()
	require.NoError(t, err, "GetResponse failed")
	id, err := response.GetInferenceId()
	require.Equal(t, dummyId, id, "expected inference id to be %s, got %s", dummyId, id)
	model, err := response.GetModel()
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", model, "expected model to be %s, got %s", "Qwen/Qwen2.5-7B-Instruct", model)
	usage, err := response.GetUsage()
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

func loadJson(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return data, nil
}

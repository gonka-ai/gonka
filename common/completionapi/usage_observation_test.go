package completionapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Usage feeds the tokens the chain is told about, so both paths must agree.
func TestProcessorUsageMatchesTheFullReParse(t *testing.T) {
	const usageChunk = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[],` +
		`"usage":{"prompt_tokens":31,"completion_tokens":10,"total_tokens":41}}`
	const laterUsageChunk = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[],` +
		`"usage":{"prompt_tokens":999,"completion_tokens":999,"total_tokens":1998}}`
	const logprobChunk = `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,` +
		`"delta":{"content":" word"},"logprobs":{"content":[{"token":" word","logprob":-0.5,"bytes":[32,119,111,114,100],` +
		`"top_logprobs":[{"token":" word","logprob":-0.5,"bytes":[32,119,111,114,100]}]}]}}]}`

	for name, chunks := range map[string][]string{
		"usage chunk arrived":     {logprobChunk, usageChunk, "data: [DONE]"},
		"first usage wins":        {logprobChunk, usageChunk, laterUsageChunk, "data: [DONE]"},
		"stream cut before usage": {logprobChunk, logprobChunk, logprobChunk},
		"nothing but done":        {"data: [DONE]"},
		"malformed usage before a good one": {`data: {"id":"c","choices":[],"usage":{"prompt_tokens":"nope","completion_tokens":1}}`,
			usageChunk, "data: [DONE]"},
	} {
		t.Run(name, func(t *testing.T) {
			reference := NewExecutorResponseProcessor("devshard-1-1", false)
			observing := NewExecutorResponseProcessor("devshard-1-1", false)
			for _, chunk := range chunks {
				_, err := reference.ProcessStreamedResponse(chunk)
				require.NoError(t, err)
				_, err = observing.ProcessStreamedResponse(chunk)
				require.NoError(t, err)
			}

			wantUsage, wantErr := usageFromFullReParse(reference)
			gotUsage, gotErr := observing.GetUsage()

			require.Equal(t, wantErr != nil, gotErr != nil, "want %v, got %v", wantErr, gotErr)
			require.Equal(t, wantUsage, gotUsage)
		})
	}
}

func TestProcessorUsageMatchesTheFullReParseForJSON(t *testing.T) {
	for name, body := range map[string]string{
		"usage present": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":14,"completion_tokens":2}}`,
		"usage missing": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[]}`,
		"usage zeroed": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
		"count as a string": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":31,"completion_tokens":"10"}}`,
		"count negative": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":31,"completion_tokens":-5}}`,
		"count fractional": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":31,"completion_tokens":10.7}}`,
		"count minus zero": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":-0,"completion_tokens":5}}`,
		"count past int64": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"prompt_tokens":18446744073709551615,"completion_tokens":5}}`,
		"total tokens only": `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[],` +
			`"usage":{"total_tokens":41}}`,
	} {
		t.Run(name, func(t *testing.T) {
			reference := NewExecutorResponseProcessor("devshard-1-1", false)
			observing := NewExecutorResponseProcessor("devshard-1-1", false)
			_, err := reference.ProcessJsonResponse([]byte(body))
			require.NoError(t, err)
			_, err = observing.ProcessJsonResponse([]byte(body))
			require.NoError(t, err)

			wantUsage, wantErr := usageFromFullReParse(reference)
			gotUsage, gotErr := observing.GetUsage()

			require.Equal(t, wantErr != nil, gotErr != nil, "want %v, got %v", wantErr, gotErr)
			require.Equal(t, wantUsage, gotUsage)
		})
	}
}

func TestProcessorUsageRefusesAnEmptyProcessor(t *testing.T) {
	_, err := NewExecutorResponseProcessor("devshard-1-1", false).GetUsage()

	require.ErrorIs(t, err, ErrNoResponseCollected)
}

func TestGetResponseBytesRefusesAnEmptyProcessor(t *testing.T) {
	bytes, err := NewExecutorResponseProcessor("devshard-1-1", false).GetResponseBytes()

	require.ErrorIs(t, err, ErrNoResponseCollected)
	require.Nil(t, bytes)
}

func usageFromFullReParse(processor *ExecutorResponseProcessor) (*Usage, error) {
	response, err := processor.GetResponse()
	if err != nil {
		return nil, err
	}
	return response.GetUsage()
}

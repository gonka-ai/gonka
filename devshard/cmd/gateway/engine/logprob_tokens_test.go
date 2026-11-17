package engine

import "testing"

// Test flow:
//  1. Classify each table case's chunk: a token-id replay, decoded text, a negative number, a chunk with no logprobs, and the `[DONE]` terminator.
//  2. For each case, assert `LogprobsDecoded` matches the case's expected verdict.
func TestLogprobTokensAreReadAsIdsOrText(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		chunk string
		want  bool
	}{
		{
			name:  "token ids replay",
			chunk: `data: {"choices":[{"logprobs":{"content":[{"token":"1917","logprob":-0.1}]}}]}` + "\n",
			want:  false,
		},
		{
			name:  "decoded text cannot",
			chunk: `data: {"choices":[{"logprobs":{"content":[{"token":" hello","logprob":-0.1}]}}]}` + "\n",
			want:  true,
		},
		{
			name:  "a negative number is not an id either",
			chunk: `data: {"choices":[{"logprobs":{"content":[{"token":"-4","logprob":-0.1}]}}]}` + "\n",
			want:  true,
		},
		{
			name:  "a chunk carrying no logprobs judges nothing",
			chunk: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n",
			want:  false,
		},
		{
			name:  "the terminator judges nothing",
			chunk: "data: [DONE]\n",
			want:  false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyChunk([]byte(testCase.chunk), false).LogprobsDecoded; got != testCase.want {
				t.Fatalf("LogprobsDecoded = %v, want %v", got, testCase.want)
			}
		})
	}
}

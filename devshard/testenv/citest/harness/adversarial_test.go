package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUniquifyChatRequestChangesLastMessage(t *testing.T) {
	req := ChatCompletionRequest{
		Model: "m",
		Messages: []ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hello"},
		},
		MaxTokens: 8,
	}
	a := uniquifyChatRequest(req, 1)
	b := uniquifyChatRequest(req, 2)
	require.Equal(t, "hello", req.Messages[1].Content, "original must be unchanged")
	require.Equal(t, "sys", a.Messages[0].Content)
	require.Contains(t, a.Messages[1].Content, "hello")
	require.Contains(t, a.Messages[1].Content, "citest-attempt-1")
	require.NotEqual(t, a.Messages[1].Content, b.Messages[1].Content)
}

func TestUniquifyChatRequestEmptyMessages(t *testing.T) {
	req := ChatCompletionRequest{Model: "m"}
	out := uniquifyChatRequest(req, 3)
	require.Len(t, out.Messages, 1)
	require.Contains(t, out.Messages[0].Content, "citest-unique-3")
}

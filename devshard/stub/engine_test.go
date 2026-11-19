package stub

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/cmd/gateway/filters"
)

// Test flow:
//  1. Build the default stub engine and one that echoes the request.
//  2. Execute one request on each.
//  3. Assert the gateway would cache both answers, so each one finished.
func TestInferenceEngine_AnswersAreFinished(t *testing.T) {
	echoing := NewInferenceEngine()
	echoing.EchoRequest = true

	for name, engine := range map[string]*InferenceEngine{"default": NewInferenceEngine(), "echo": echoing} {
		result, err := engine.Execute(context.Background(), devshard.ExecuteRequest{Prompt: []byte(`{"messages":[]}`)})
		require.NoError(t, err, name)
		require.Equal(t, filters.CacheStorable, filters.CacheRefusal(http.StatusOK, result.ResponseBody), name)
	}
}

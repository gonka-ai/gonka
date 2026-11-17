package vocabulary

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubModelSource struct {
	hfRepo   string
	hfCommit string
	err      error
}

func (source stubModelSource) GetModelSource(context.Context, uint64, string) (string, string, error) {
	return source.hfRepo, source.hfCommit, source.err
}

func newTestVocabularyResolver(t *testing.T, source ModelSource, configBody string) (*HuggingFaceResolver, *atomic.Int32) {
	t.Helper()
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fetches.Add(1)
		if request.URL.Path != "/org/model/resolve/abc123/config.json" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write([]byte(configBody))
	}))
	t.Cleanup(server.Close)
	resolver := NewResolver(source)
	resolver.baseURL = server.URL
	return resolver, &fetches
}

var pinnedModelSource = stubModelSource{hfRepo: "org/model", hfCommit: "abc123"}

// Test flow:
// 1. The chain pins org/model@abc123 and its config.json carries vocab_size at the root or under text_config.
// 2. Resolve follows vLLM's hf_text_config: text_config's vocab_size when text_config exists, else the root value.
// 3. A config vLLM would size from class defaults, or one that is not JSON, is unknown (0).
func TestVocabularyResolver_ReadsPinnedConfig(t *testing.T) {
	cases := []struct {
		name       string
		configBody string
		want       int
	}{
		{"root vocab_size", `{"vocab_size": 200064}`, 200064},
		{"text_config wins over root", `{"vocab_size": 1, "text_config": {"vocab_size": 163840}}`, 163840},
		{"text_config without vocab_size is unknown", `{"vocab_size": 1, "text_config": {"hidden_size": 7168}}`, 0},
		{"no vocab_size is unknown", `{"hidden_size": 7168}`, 0},
		{"malformed config is unknown", `not json`, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolver, _ := newTestVocabularyResolver(t, pinnedModelSource, testCase.configBody)
			assert.Equal(t, testCase.want, resolver.Resolve(context.Background(), 1, "test-model"))
		})
	}
}

// Test flow:
// 1. Many validations of one (epoch, model) resolve at once on a cold cache.
// 2. They share a single config fetch and a later Resolve is served from the cache.
func TestVocabularyResolver_ConcurrentResolvesShareOneFetch(t *testing.T) {
	resolver, fetches := newTestVocabularyResolver(t, pinnedModelSource, `{"vocab_size": 129280}`)

	var waitGroup sync.WaitGroup
	for range 8 {
		waitGroup.Go(func() {
			assert.Equal(t, 129280, resolver.Resolve(context.Background(), 1, "test-model"))
		})
	}
	waitGroup.Wait()
	require.Equal(t, 129280, resolver.Resolve(context.Background(), 1, "test-model"))

	assert.Equal(t, int32(1), fetches.Load())
}

// Test flow:
// 1. The validation that triggers the fetch has already been cancelled.
// 2. The fetch runs detached from it, so the vocab size is still resolved and cached.
func TestVocabularyResolver_CancelledCallerStillResolves(t *testing.T) {
	resolver, fetches := newTestVocabularyResolver(t, pinnedModelSource, `{"vocab_size": 154880}`)
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Equal(t, 154880, resolver.Resolve(cancelledContext, 1, "test-model"))
	assert.Equal(t, 154880, resolver.Resolve(context.Background(), 1, "test-model"))

	assert.Equal(t, int32(1), fetches.Load())
}

// Test flow:
// 1. The chain lookup fails or the pinned config is missing on the hub.
// 2. Resolve reports an unknown vocab (0) and a second call inside the retry interval does not fetch again.
// 3. Once the retry interval passes, the next Resolve fetches again.
func TestVocabularyResolver_FailureIsUnknownAndRetriedAfterInterval(t *testing.T) {
	cases := []struct {
		name              string
		source            stubModelSource
		wantFetchesBefore int32
		wantFetchesAfter  int32
	}{
		{"chain lookup fails", stubModelSource{err: errors.New("chain down")}, 0, 0},
		{"config missing on hub", stubModelSource{hfRepo: "org/missing", hfCommit: "abc123"}, 1, 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resolver, fetches := newTestVocabularyResolver(t, testCase.source, `{"vocab_size": 129280}`)
			currentTime := time.Unix(1_000_000, 0)
			resolver.now = func() time.Time { return currentTime }

			assert.Equal(t, 0, resolver.Resolve(context.Background(), 1, "test-model"))
			assert.Equal(t, 0, resolver.Resolve(context.Background(), 1, "test-model"))
			assert.Equal(t, testCase.wantFetchesBefore, fetches.Load())

			currentTime = currentTime.Add(vocabularyRetryInterval)
			assert.Equal(t, 0, resolver.Resolve(context.Background(), 1, "test-model"))
			assert.Equal(t, testCase.wantFetchesAfter, fetches.Load())
		})
	}
}

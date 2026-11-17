package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/tokenizer"
)

type counterPromptTokens struct{ counter *tokenizer.Counter }

func (c counterPromptTokens) Prompt(_ string, body []byte) (PromptText, bool) {
	return TokenizerPrompt(c.counter, body)
}

var benchPromptSizes = []struct {
	name  string
	bytes int
}{{"200B", 200}, {"1MiB", 1 << 20}}

func benchCounters(b *testing.B) map[string]*tokenizer.Counter {
	b.Helper()
	dir := os.Getenv("GATEWAY_TOKENIZER_DIR")
	if dir == "" {
		b.Skip("GATEWAY_TOKENIZER_DIR names no directory of tokenizer.json files")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	counters := make(map[string]*tokenizer.Counter, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		counter, err := tokenizer.Load(file)
		_ = file.Close()
		if err != nil {
			b.Fatal(err)
		}
		counters[strings.TrimSuffix(filepath.Base(path), ".json")] = counter
	}
	return counters
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR, and skip when it names none.
//  2. Build the benchmark server with qwen pinned to the largest context, so every request is served, and the stats tokenizer on, so every prompt is counted after the cache.
//  3. Serve a buffered chat request of a 200 B and a 1 MiB prompt, with no tokenizer and with each, and report the handler's time and allocations.
func BenchmarkChatPromptCount(b *testing.B) {
	estimators := map[string]PromptTokens{"off": nil}
	for name, counter := range benchCounters(b) {
		estimators[name] = counterPromptTokens{counter: counter}
	}
	pinned := int64(config.MaxContextTokens)
	for _, size := range benchPromptSizes {
		body := benchChatBody(false, size.bytes)
		for name, estimator := range estimators {
			b.Run(size.name+"/"+name, func(b *testing.B) {
				server := benchServer(b, benchChunks(48, 24))
				next := *server.config.Load()
				next.Limits.ModelLimits = map[string]config.ModelLimits{"qwen": {MaxModelLen: &pinned}}
				server.config.Swap(&next)
				server.promptTokens = estimator
				server.statsTokenizerEnabled = true
				serveRepeatedly(b, server.Handler(), benchRequest("/v1/chat/completions", body), body, http.StatusOK)
			})
		}
	}
}

// Test flow:
//  1. Load every tokenizer.json in GATEWAY_TOKENIZER_DIR, and skip when it names none.
//  2. For a chat body of a 200 B and a 1 MiB prompt and each tokenizer, read the prompt and count it.
//  3. Report the time and allocations of the one count a request takes, without the handler around it.
func BenchmarkPromptCountCost(b *testing.B) {
	for name, counter := range benchCounters(b) {
		prompts := counterPromptTokens{counter: counter}
		for _, size := range benchPromptSizes {
			body := []byte(benchChatBody(false, size.bytes))
			b.Run(size.name+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					text, known := prompts.Prompt("qwen", body)
					if !known {
						b.Fatal("the prompt has no text")
					}
					if _, known := text.Count(); !known {
						b.Fatal("the prompt could not be tokenized")
					}
				}
			})
		}
	}
}

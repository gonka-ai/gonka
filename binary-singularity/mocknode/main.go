// Mock inference node for Binary Singularity bookworm experiments.
//
// Exposes the same API surface as the real mlnode:
//   - POST /v1/chat/completions — returns realistic mock responses
//   - GET /health — health check
//
// Responses are deterministic (seeded by prompt hash) so experiments are reproducible.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"
)

var port = "8080"

func init() {
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
}

type ChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	MaxTokens int `json:"max_tokens"`
}

type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

var mockResponses = map[string]string{
	"fibonacci": `Here's an iterative Fibonacci implementation in Go:

` + "```go" + `
func fibonacci(n int) int {
    if n <= 1 {
        return n
    }
    a, b := 0, 1
    for i := 2; i <= n; i++ {
        a, b = b, a+b
    }
    return b
}
` + "```" + `

This runs in O(n) time and O(1) space.`,

	"race": `To fix the race condition, wrap the shared counter with a sync.Mutex:

` + "```go" + `
type SafeCounter struct {
    mu sync.Mutex
    v  map[string]int
}

func (c *SafeCounter) Inc(key string) {
    c.mu.Lock()
    c.v[key]++
    c.mu.Unlock()
}

func (c *SafeCounter) Value(key string) int {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.v[key]
}
` + "```" + `

Run ` + "`go test -race ./...`" + ` to verify the fix.`,

	"http": `Here's a basic HTTP middleware pattern in Go:

` + "```go" + `
func rateLimiter(next http.Handler) http.Handler {
    limiter := rate.NewLimiter(10, 30) // 10 req/s, burst 30
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !limiter.Allow() {
            http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
            return
        }
        next.ServeHTTP(w, r)
    })
}
` + "```",

	"docker": `Docker Compose setup for the service:

` + "```yaml" + `
services:
  app:
    build: .
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://db:5432/app
    depends_on:
      - db
  db:
    image: postgres:16-bookworm
    environment:
      - POSTGRES_DB=app
      - POSTGRES_PASSWORD=secret
` + "```",

	"default": `I'll analyze the task and provide a solution.

The key approach is:
1. Identify the core problem pattern
2. Apply the appropriate fix/implementation
3. Verify with tests

Let me implement this step by step.`,
}

func main() {
	http.HandleFunc("/v1/chat/completions", handleChat)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "type": "mock"})
	})

	fmt.Printf("[mock-inference] Starting on port %s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	prompt := ""
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			prompt = msg.Content
			break
		}
	}

	// Simulate GPU latency (50–200ms for mock)
	hash := sha256.Sum256([]byte(prompt))
	rng := rand.New(rand.NewSource(int64(hash[0])<<8 | int64(hash[1])))
	latency := 50 + rng.Intn(150)
	time.Sleep(time.Duration(latency) * time.Millisecond)

	content := selectResponse(prompt)

	resp := ChatResponse{
		ID:      fmt.Sprintf("mock-%x", hash[:4]),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
	}
	resp.Choices = append(resp.Choices, struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{
		Index:        0,
		FinishReason: "stop",
	})
	resp.Choices[0].Message.Role = "assistant"
	resp.Choices[0].Message.Content = content
	resp.Usage.PromptTokens = len(prompt) / 4
	resp.Usage.CompletionTokens = len(content) / 4
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func selectResponse(prompt string) string {
	keywords := []string{"fibonacci", "race", "http", "docker"}
	for _, kw := range keywords {
		if containsStr(prompt, kw) {
			return mockResponses[kw]
		}
	}
	return mockResponses["default"]
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

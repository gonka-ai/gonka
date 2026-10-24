package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devshard/testenv/mockopenai"
)

func main() {
	cfg := mockopenai.DefaultConfig()
	cfg.Addr = envOr("MOCK_OPENAI_ADDR", ":8088")
	cfg.ReplayFile = envOr("MOCK_OPENAI_REPLAY_FILE", "")
	cfg.Faults = faultsFromEnv()
	cfg.Workers = intFromEnv("MOCK_OPENAI_WORKERS")
	cfg.Queue = intFromEnv("MOCK_OPENAI_QUEUE")
	if raw := os.Getenv("MOCK_OPENAI_CHAOS_JSON"); raw != "" {
		// The chaos object of POST /testenv/fault, on from the start: a node that answers some of its
		// executions dishonestly for the whole run.
		var c mockopenai.ChaosConfig
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			log.Fatalf("mock-openai: MOCK_OPENAI_CHAOS_JSON: %v", err)
		}
		c.Enabled = true
		if err := c.Validate(); err != nil {
			log.Fatalf("mock-openai: MOCK_OPENAI_CHAOS_JSON: %v", err)
		}
		cfg.Chaos = c
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := mockopenai.NewServerWithError(cfg)
	if err != nil {
		log.Fatalf("mock-openai: %v", err)
	}
	if raw := os.Getenv("MOCK_OPENAI_RULES_JSON"); raw != "" {
		var rules []mockopenai.FaultRule
		if err := json.Unmarshal([]byte(raw), &rules); err != nil {
			log.Fatalf("mock-openai: MOCK_OPENAI_RULES_JSON: %v", err)
		}
		if err := srv.SetRules(rules); err != nil {
			log.Fatalf("mock-openai: MOCK_OPENAI_RULES_JSON: %v", err)
		}
	}
	log.Printf("mock-openai on %s", cfg.Addr)
	if err := srv.Serve(ctx, cfg.Addr); err != nil && err != context.Canceled {
		log.Fatalf("mock-openai: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func faultsFromEnv() mockopenai.FaultConfig {
	f := mockopenai.DefaultConfig().Faults
	// TTFT delays the first SSE chunk only. latency_ms delays Validate JSON.
	if v := os.Getenv("MOCK_OPENAI_TTFT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.StreamFirstTokenDelay = d
		}
	}
	if v := os.Getenv("MOCK_OPENAI_LATENCY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			f.Latency = time.Duration(ms) * time.Millisecond
		}
	}
	if v := os.Getenv("MOCK_OPENAI_HTTP_STATUS"); v != "" {
		if code, err := strconv.Atoi(v); err == nil {
			f.HTTPStatus = code
		}
	}
	if v := os.Getenv("MOCK_OPENAI_FAILURE_RATE"); v != "" {
		if rate, err := strconv.ParseFloat(v, 64); err == nil {
			f.FailureRate = rate
		}
	}
	if envTruthy("MOCK_OPENAI_DROP_FIRST_CHUNK") {
		f.DropFirstChunk = true
	}
	if envTruthy("MOCK_OPENAI_PARTIAL_STREAM") {
		f.PartialStream = true
	}
	if v := os.Getenv("MOCK_OPENAI_SSE_ERROR_MESSAGE"); v != "" {
		f.SSEErrorMessage = v
	}
	if v := os.Getenv("MOCK_OPENAI_STREAM_CHUNK_DELAY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			f.StreamChunkDelay = time.Duration(ms) * time.Millisecond
		}
	}
	if v := os.Getenv("MOCK_OPENAI_TOKEN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.StreamChunkDelay = d
		}
	}
	// The *_P99 values switch TTFT and token interval to seeded lognormal draws.
	if v := os.Getenv("MOCK_OPENAI_TTFT_P99"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.StreamFirstTokenP99 = d
		}
	}
	if v := os.Getenv("MOCK_OPENAI_TOKEN_INTERVAL_P99"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			f.StreamChunkP99 = d
		}
	}
	if v := os.Getenv("MOCK_OPENAI_JITTER_SEED"); v != "" {
		if seed, err := strconv.ParseUint(v, 10, 64); err == nil {
			f.JitterSeed = seed
		}
	}
	if envTruthy("MOCK_OPENAI_HANG") {
		f.Hang = true
	}
	if envTruthy("MOCK_OPENAI_ORACLE") {
		f.Oracle = true
	}
	if envTruthy("MOCK_OPENAI_HONEST_USAGE") {
		f.HonestUsage = true
	}
	if n, ok := envInt("MOCK_OPENAI_CHUNK_TOKENS"); ok {
		f.ChunkTokens = n
	}
	if n, ok := envInt("MOCK_OPENAI_FIRST_CONTENT_MS"); ok {
		f.FirstContentDelay = time.Duration(n) * time.Millisecond
	}
	if n, ok := envInt("MOCK_OPENAI_EXEC_MS"); ok {
		f.ExecDuration = time.Duration(n) * time.Millisecond
	}
	if n, ok := envInt("MOCK_OPENAI_REPLAY_MS"); ok {
		f.ReplayDelay = time.Duration(n) * time.Millisecond
	}
	return f
}

func intFromEnv(key string) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// envInt reads a non-negative integer knob; a value that is set but not one stops the mock, so a typo
// does not run the node without the shape it was asked for.
func envInt(key string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Fatalf("mock-openai: %s=%q must be a non-negative integer", key, os.Getenv(key))
	}
	return n, true
}

func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

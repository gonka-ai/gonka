package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"devshard/testenv/mockopenai"
)

func main() {
	cfg := mockopenai.DefaultConfig()
	cfg.Addr = envOr("MOCK_OPENAI_ADDR", ":8088")
	cfg.Faults = faultsFromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := mockopenai.NewServer(cfg)
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
	if v := os.Getenv("MOCK_OPENAI_DROP_FIRST_CHUNK"); v == "1" || v == "true" {
		f.DropFirstChunk = true
	}
	if v := os.Getenv("MOCK_OPENAI_PARTIAL_STREAM"); v == "1" || v == "true" {
		f.PartialStream = true
	}
	if v := os.Getenv("MOCK_OPENAI_STREAM_CHUNK_DELAY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			f.StreamChunkDelay = time.Duration(ms) * time.Millisecond
		}
	}
	return f
}

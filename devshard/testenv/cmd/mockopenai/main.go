package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"devshard/internal/boolvalue"
	"devshard/testenv/mockopenai"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("mock-openai: %v", err)
	}
}

func run() error {
	cfg := mockopenai.DefaultConfig()
	cfg.Addr = envOr("MOCK_OPENAI_ADDR", ":8088")
	cfg.Faults = faultsFromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := mockopenai.NewServer(cfg)
	log.Printf("mock-openai on %s", cfg.Addr)
	if err := srv.Serve(ctx, cfg.Addr); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
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
	if envTruthy("MOCK_OPENAI_DROP_FIRST_CHUNK") {
		f.DropFirstChunk = true
	}
	if envTruthy("MOCK_OPENAI_PARTIAL_STREAM") {
		f.PartialStream = true
	}
	if v := os.Getenv("MOCK_OPENAI_STREAM_CHUNK_DELAY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			f.StreamChunkDelay = time.Duration(ms) * time.Millisecond
		}
	}
	return f
}

func envTruthy(key string) bool {
	enabled, err := boolvalue.Parse(os.Getenv(key))
	return err == nil && enabled
}

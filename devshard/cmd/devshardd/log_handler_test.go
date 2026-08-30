package main

import (
	"log/slog"
	"testing"
)

func TestSlogLevelFromEnv_DefaultInfo(t *testing.T) {
	t.Setenv("DEVSHARD_LOG_LEVEL", "")
	if got := slogLevelFromEnv(); got != slog.LevelInfo {
		t.Fatalf("empty env: got %v want Info", got)
	}
}

func TestSlogLevelFromEnv_Debug(t *testing.T) {
	t.Setenv("DEVSHARD_LOG_LEVEL", "debug")
	if got := slogLevelFromEnv(); got != slog.LevelDebug {
		t.Fatalf("debug: got %v", got)
	}
}

func TestSlogLevelFromEnv_UnknownKeepsInfo(t *testing.T) {
	t.Setenv("DEVSHARD_LOG_LEVEL", "trace")
	if got := slogLevelFromEnv(); got != slog.LevelInfo {
		t.Fatalf("unknown: got %v want Info", got)
	}
}

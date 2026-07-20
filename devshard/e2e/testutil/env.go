package testutil

import (
	"os"
	"strings"
	"testing"
	"time"
)

const DefaultRequestTimeout = 2 * time.Minute

var HostPrivateKeys = []string{
	"0000000000000000000000000000000000000000000000000000000000000011",
	"0000000000000000000000000000000000000000000000000000000000000012",
	"0000000000000000000000000000000000000000000000000000000000000013",
}

const UserPrivateKey = "0000000000000000000000000000000000000000000000000000000000000021"

const AdminAPIKey = "devshard-e2e-admin-key"

func EnvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func DebugEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("DEVSHARD_E2E_DEBUG")))
	return value == "1" || value == "true" || value == "yes"
}

func DebugLogf(t *testing.T, format string, args ...any) {
	t.Helper()
	if DebugEnabled() {
		t.Logf(format, args...)
	}
}

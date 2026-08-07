package observability_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeployJoinNeverSetsPayloadFull guards the T4a privacy rule: production
// compose/env templates must never enable DEVSHARD_LOG_PAYLOADS=full.
func TestDeployJoinNeverSetsPayloadFull(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "join")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	var hits []string
	for _, ent := range entries {
		name := ent.Name()
		switch {
		case strings.HasSuffix(name, ".yml"),
			strings.HasSuffix(name, ".yaml"),
			strings.HasSuffix(name, ".env"),
			strings.HasSuffix(name, ".template"),
			strings.Contains(name, "env"):
		default:
			continue
		}
		path := filepath.Join(root, name)
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		text := string(body)
		if strings.Contains(text, "DEVSHARD_LOG_PAYLOADS=full") ||
			strings.Contains(text, "DEVSHARD_LOG_PAYLOADS: full") ||
			strings.Contains(text, "DEVSHARD_LOG_PAYLOADS: \"full\"") {
			hits = append(hits, path)
		}
	}
	require.Empty(t, hits, "deploy/join must not set DEVSHARD_LOG_PAYLOADS=full: %v", hits)
}

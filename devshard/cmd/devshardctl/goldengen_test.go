//go:build goldengen

package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateFilterGoldens runs every corpus case through the OLD pipeline and
// writes the golden the NEW cmd/gateway/filters package must reproduce
// byte-for-byte. Run manually: go test ./cmd/devshardctl/ -tags goldengen -run TestGenerateFilterGoldens -count=1
type goldenCorpusCase struct {
	RoutedModel      string          `json:"routed_model"`
	Admin            bool            `json:"admin"`
	DefaultMaxTokens uint64          `json:"default_max_tokens"`
	MaxTokensCap     uint64          `json:"max_tokens_cap"`
	Body             json.RawMessage `json:"body"`
}

func TestGenerateFilterGoldens(t *testing.T) {
	corpusDir := filepath.Join("..", "gateway", "filters", "testdata", "corpus")
	goldensDir := filepath.Join("..", "gateway", "filters", "testdata", "goldens")
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("reading corpus dir: %v", err)
	}
	if err := os.MkdirAll(goldensDir, 0o755); err != nil {
		t.Fatalf("creating goldens dir: %v", err)
	}
	generated := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(corpusDir, entry.Name()))
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		var corpusCase goldenCorpusCase
		if err := json.Unmarshal(raw, &corpusCase); err != nil {
			t.Fatalf("%s: parsing case: %v", entry.Name(), err)
		}
		limits := outputTokenLimits{DefaultMaxTokens: corpusCase.DefaultMaxTokens, MaxTokensCap: corpusCase.MaxTokensCap}
		normalizedBody, _, err := normalizeChatRequestForAuthAndLimits(corpusCase.Body, corpusCase.Admin, limits, corpusCase.RoutedModel)
		var golden map[string]any
		if err != nil {
			golden = map[string]any{
				"status":      "rejected",
				"http_status": chatRequestErrorStatus(err, 400),
				"message":     err.Error(),
			}
		} else {
			golden = map[string]any{
				"status":      "ok",
				"body_base64": base64.StdEncoding.EncodeToString(normalizedBody),
			}
		}
		encoded, err := json.MarshalIndent(golden, "", "  ")
		if err != nil {
			t.Fatalf("%s: encoding golden: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(goldensDir, entry.Name()), append(encoded, '\n'), 0o644); err != nil {
			t.Fatalf("%s: writing golden: %v", entry.Name(), err)
		}
		generated++
	}
	t.Logf("generated %d goldens", generated)
	if generated == 0 {
		t.Fatal("no corpus cases found — corpus must exist before generating")
	}
}

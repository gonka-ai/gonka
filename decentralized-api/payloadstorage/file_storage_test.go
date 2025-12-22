package payloadstorage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStorage_StoreRetrieve(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir)
	ctx := context.Background()

	prompt := []byte(`{"model":"test","seed":123,"messages":[{"role":"user","content":"hello"}]}`)
	response := []byte(`{"id":"inf-1","choices":[{"message":{"content":"hi"}}]}`)

	if err := storage.Store(ctx, "inf-1", 5, prompt, response); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	gotPrompt, gotResponse, err := storage.Retrieve(ctx, "inf-1", 5)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	if !bytes.Equal(gotPrompt, prompt) {
		t.Errorf("prompt mismatch: got %q, want %q", gotPrompt, prompt)
	}
	if !bytes.Equal(gotResponse, response) {
		t.Errorf("response mismatch: got %q, want %q", gotResponse, response)
	}
}

func TestFileStorage_RetrieveNotFound(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir)
	ctx := context.Background()

	_, _, err := storage.Retrieve(ctx, "nonexistent", 1)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFileStorage_RetrieveWrongEpoch(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir)
	ctx := context.Background()

	// Store in epoch 5
	if err := storage.Store(ctx, "inf-1", 5, []byte("prompt"), []byte("response")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Try to retrieve from wrong epoch
	_, _, err := storage.Retrieve(ctx, "inf-1", 10)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for wrong epoch, got %v", err)
	}

	// Verify correct epoch still works
	_, _, err = storage.Retrieve(ctx, "inf-1", 5)
	if err != nil {
		t.Errorf("retrieve from correct epoch should work: %v", err)
	}
}

func TestFileStorage_PruneEpoch(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir)
	ctx := context.Background()

	if err := storage.Store(ctx, "inf-1", 10, []byte("prompt1"), []byte("response1")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if err := storage.Store(ctx, "inf-2", 10, []byte("prompt2"), []byte("response2")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if err := storage.Store(ctx, "inf-3", 11, []byte("prompt3"), []byte("response3")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	if err := storage.PruneEpoch(ctx, 10); err != nil {
		t.Fatalf("PruneEpoch failed: %v", err)
	}

	_, _, err := storage.Retrieve(ctx, "inf-1", 10)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for pruned inference, got %v", err)
	}

	_, _, err = storage.Retrieve(ctx, "inf-3", 11)
	if err != nil {
		t.Errorf("epoch 11 should not be pruned: %v", err)
	}
}

func TestFileStorage_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir)
	ctx := context.Background()

	if err := storage.Store(ctx, "inf-1", 1, []byte("prompt"), []byte("response")); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Verify no temp files left
	epochDir := filepath.Join(dir, "1")
	entries, err := os.ReadDir(epochDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestComputePromptHash_Reproducible(t *testing.T) {
	payload := []byte(`{"seed":123,"model":"test","messages":[{"role":"user","content":"hello"}]}`)

	hash1, err := ComputePromptHash(payload)
	if err != nil {
		t.Fatalf("ComputePromptHash failed: %v", err)
	}
	hash2, err := ComputePromptHash(payload)
	if err != nil {
		t.Fatalf("ComputePromptHash failed: %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("hash not reproducible: %s != %s", hash1, hash2)
	}
}

func TestComputePromptHash_KeyOrderIndependent(t *testing.T) {
	payload1 := []byte(`{"model":"test","seed":123}`)
	payload2 := []byte(`{"seed":123,"model":"test"}`)

	hash1, err := ComputePromptHash(payload1)
	if err != nil {
		t.Fatalf("ComputePromptHash failed: %v", err)
	}
	hash2, err := ComputePromptHash(payload2)
	if err != nil {
		t.Fatalf("ComputePromptHash failed: %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("hash should be key-order independent: %s != %s", hash1, hash2)
	}
}

func TestComputeResponseHash_Reproducible(t *testing.T) {
	payload := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello world"}}]}`)

	hash1, err := ComputeResponseHash(payload)
	if err != nil {
		t.Fatalf("ComputeResponseHash failed: %v", err)
	}
	hash2, err := ComputeResponseHash(payload)
	if err != nil {
		t.Fatalf("ComputeResponseHash failed: %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("hash not reproducible: %s != %s", hash1, hash2)
	}
}

func TestComputeResponseHash_HashesFullPayload(t *testing.T) {
	// Different payloads should produce different hashes (even if content is same)
	payload1 := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"}}]}`)
	payload2 := []byte(`{"id":"inf-2","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"}}]}`)

	hash1, err := ComputeResponseHash(payload1)
	if err != nil {
		t.Fatalf("ComputeResponseHash failed: %v", err)
	}
	hash2, err := ComputeResponseHash(payload2)
	if err != nil {
		t.Fatalf("ComputeResponseHash failed: %v", err)
	}
	if hash1 == hash2 {
		t.Errorf("different payloads should produce different hashes: %s == %s", hash1, hash2)
	}
}

func TestComputeResponseHash_IncludesLogprobs(t *testing.T) {
	// Same content but different logprobs must produce different hashes
	// This prevents the attack where executor serves fake logprobs with valid content
	payloadWithLogprobs := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"logprobs":{"content":[{"token":"Hello","logprob":-0.5,"top_logprobs":[{"token":"Hello","logprob":-0.5}]}]}}]}`)
	payloadWithFakeLogprobs := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"logprobs":{"content":[{"token":"Hello","logprob":-0.1,"top_logprobs":[{"token":"Hello","logprob":-0.1}]}]}}]}`)

	hash1, err := ComputeResponseHash(payloadWithLogprobs)
	if err != nil {
		t.Fatalf("ComputeResponseHash failed: %v", err)
	}
	hash2, err := ComputeResponseHash(payloadWithFakeLogprobs)
	if err != nil {
		t.Fatalf("ComputeResponseHash failed: %v", err)
	}
	if hash1 == hash2 {
		t.Errorf("payloads with different logprobs must produce different hashes: %s == %s", hash1, hash2)
	}
}

func TestFullCycle_HashIntegrity(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir)
	ctx := context.Background()

	prompt := []byte(`{"model":"test","seed":42,"messages":[{"role":"user","content":"What is 2+2?"}]}`)
	response := []byte(`{"id":"inf-123","choices":[{"index":0,"message":{"role":"assistant","content":"The answer is 4."}}]}`)

	// Compute hashes before storage
	promptHashBefore, err := ComputePromptHash(prompt)
	if err != nil {
		t.Fatalf("ComputePromptHash before store failed: %v", err)
	}
	responseHashBefore, err := ComputeResponseHash(response)
	if err != nil {
		t.Fatalf("ComputeResponseHash before store failed: %v", err)
	}

	// Store
	if err := storage.Store(ctx, "inf-123", 1, prompt, response); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Load
	loadedPrompt, loadedResponse, err := storage.Retrieve(ctx, "inf-123", 1)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Compute hashes after load
	promptHashAfter, err := ComputePromptHash(loadedPrompt)
	if err != nil {
		t.Fatalf("ComputePromptHash after load failed: %v", err)
	}
	responseHashAfter, err := ComputeResponseHash(loadedResponse)
	if err != nil {
		t.Fatalf("ComputeResponseHash after load failed: %v", err)
	}

	// Verify hash integrity
	if promptHashBefore != promptHashAfter {
		t.Errorf("prompt hash mismatch: before=%s after=%s", promptHashBefore, promptHashAfter)
	}
	if responseHashBefore != responseHashAfter {
		t.Errorf("response hash mismatch: before=%s after=%s", responseHashBefore, responseHashAfter)
	}
}

func TestFileStorage_FilesystemUnsafeInferenceIds(t *testing.T) {
	tests := []struct {
		name        string
		inferenceId string
	}{
		{
			name:        "base64 with forward slashes",
			inferenceId: "5J3sjenocIRq76fpvIV6Cxuo+DNBypuRRectxzWvDb0vPzvja5JmCtGm1ag2s0zoAi2hDI6/NoOXX0cWF/PnRw==",
		},
		{
			name:        "base64 with plus signs",
			inferenceId: "abc+def+ghi+jkl==",
		},
		{
			name:        "base64 with multiple slashes",
			inferenceId: "a/b/c/d/e/f/g==",
		},
		{
			name:        "mixed unsafe characters",
			inferenceId: "a+b/c+d/e==",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			storage := NewFileStorage(dir)
			ctx := context.Background()

			prompt := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`)
			response := []byte(`{"choices":[{"message":{"content":"hi"}}]}`)

			if err := storage.Store(ctx, tt.inferenceId, 5, prompt, response); err != nil {
				t.Fatalf("Store failed for inferenceId %q: %v", tt.inferenceId, err)
			}

			gotPrompt, gotResponse, err := storage.Retrieve(ctx, tt.inferenceId, 5)
			if err != nil {
				t.Fatalf("Retrieve failed for inferenceId %q: %v", tt.inferenceId, err)
			}
			if !bytes.Equal(gotPrompt, prompt) {
				t.Errorf("prompt mismatch: got %q, want %q", gotPrompt, prompt)
			}
			if !bytes.Equal(gotResponse, response) {
				t.Errorf("response mismatch: got %q, want %q", gotResponse, response)
			}
		})
	}
}

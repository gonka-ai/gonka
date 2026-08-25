package payloads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileStorage_StoreRetrieveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStorage(dir)
	ctx := context.Background()

	require.NoError(t, fs.Store(ctx, "42", 7, 3, []byte("prompt"), []byte("response")))
	prompt, response, err := fs.Retrieve(ctx, "42", 7, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("prompt"), prompt)
	require.Equal(t, []byte("response"), response)

	_, err = os.Stat(filepath.Join(dir, "3", "42", "7"+plainSuffix))
	require.NoError(t, err)
}

func TestFileStorage_RejectsPathTraversalEscrowID(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStorage(dir)
	ctx := context.Background()
	outside := filepath.Join(dir, "..", "escaped.json")

	for _, escrowID := range []string{
		"../escaped",
		"..",
		"foo/bar",
		"foo\\bar",
		"",
		" ",
		".",
	} {
		err := fs.Store(ctx, escrowID, 1, 1, []byte("p"), []byte("r"))
		require.Error(t, err, "escrowId=%q", escrowID)
		_, _, err = fs.Retrieve(ctx, escrowID, 1, 1)
		require.Error(t, err, "escrowId=%q", escrowID)
	}

	_, err := os.Stat(outside)
	require.Error(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSanitizeEscrowPathSegment(t *testing.T) {
	got, err := sanitizeEscrowPathSegment("12345")
	require.NoError(t, err)
	require.Equal(t, "12345", got)

	_, err = sanitizeEscrowPathSegment("../x")
	require.Error(t, err)
}

// A directory holds whatever earlier versions left in it, so reading must accept either suffix.
func TestFileStorageReadsBothSuffixes(t *testing.T) {
	baseDir := t.TempDir()
	ctx := context.Background()
	dir := filepath.Join(baseDir, "11", "60453")
	prompt, response := []byte(`{"messages":[]}`), []byte(`{"choices":[]}`)

	plain := NewFileStorage(baseDir)
	if err := plain.Store(ctx, "60453", 6, 11, prompt, response); err != nil {
		t.Fatalf("Store(gate off): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "6"+plainSuffix)); err != nil {
		t.Fatalf("with the gate off the file must stay plain: %v", err)
	}

	storage := NewCompressingFileStorage(baseDir)
	if err := storage.Store(ctx, "60453", 7, 11, prompt, response); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "7"+compressedSuffix)); err != nil {
		t.Fatalf("with the gate on the file must be compressed: %v", err)
	}

	// The gate governs writing only: either storage reads either file.
	for name, reader := range map[string]*FileStorage{"gate off": plain, "gate on": storage} {
		for _, inferenceID := range []uint64{6, 7} {
			if _, _, err := reader.Retrieve(ctx, "60453", inferenceID, 11); err != nil {
				t.Fatalf("%s cannot read inference %d: %v", name, inferenceID, err)
			}
		}
	}

	gotPrompt, gotResponse, err := storage.Retrieve(ctx, "60453", 7, 11)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if string(gotPrompt) != string(prompt) || string(gotResponse) != string(response) {
		t.Fatalf("compressed round trip changed the payloads: %q / %q", gotPrompt, gotResponse)
	}

	legacy, err := json.Marshal(storedPayload{PromptPayload: []byte(`{"old":true}`), ResponsePayload: []byte(`{"old":"response"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "8"+plainSuffix), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	gotPrompt, gotResponse, err = storage.Retrieve(ctx, "60453", 8, 11)
	if err != nil {
		t.Fatalf("Retrieve(plain): %v", err)
	}
	if string(gotPrompt) != `{"old":true}` || string(gotResponse) != `{"old":"response"}` {
		t.Fatalf("a payload written before compression came back as %q / %q", gotPrompt, gotResponse)
	}

	if _, _, err := storage.Retrieve(ctx, "60453", 9, 11); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Retrieve(missing) = %v, want ErrNotFound", err)
	}
}

// A small compressed file can claim any output size, so the read is bounded: one payload fails instead
// of the process dying on a file nobody could have sized in advance.
func TestFileStorageRefusesAPayloadThatInflatesPastTheBound(t *testing.T) {
	baseDir := t.TempDir()
	dir := filepath.Join(baseDir, "11", "60453")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	bomb := compressPayloadFile(make([]byte, maxPayloadFileBytes+1))
	if len(bomb) >= maxPayloadFileBytes {
		t.Fatalf("the fixture is not a bomb: %d compressed bytes", len(bomb))
	}
	if err := os.WriteFile(filepath.Join(dir, "7"+compressedSuffix), bomb, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := NewFileStorage(baseDir).Retrieve(context.Background(), "60453", 7, 11)
	require.Error(t, err, "a file inflating past the bound must be refused, not read")
	require.Contains(t, err.Error(), "bound")
}

package payloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	bomb, err := compressPayloadFile(make([]byte, maxPayloadFileBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(bomb) >= maxPayloadFileBytes {
		t.Fatalf("the fixture is not a bomb: %d compressed bytes", len(bomb))
	}
	if err := os.WriteFile(filepath.Join(dir, "7"+compressedSuffix), bomb, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = NewFileStorage(baseDir).Retrieve(context.Background(), "60453", 7, 11)
	require.Error(t, err, "a file inflating past the bound must be refused, not read")
	require.Contains(t, err.Error(), "bound")
}

// The suffix is the reader's only clue to the format, so a store that could not compress must not
// claim it did: plain JSON under the compressed name is a payload nothing can open again.
func TestAFailedEncodeIsNamedForWhatItActuallyHolds(t *testing.T) {
	plain := []byte(`{"prompt":"p"}`)
	compressed, err := compressPayloadFile(plain)
	if err != nil {
		t.Fatal(err)
	}

	body, suffix := namePayloadFile(plain, compressed, nil, 7)
	if suffix != compressedSuffix || string(body) != string(compressed) {
		t.Fatalf("a successful encode must be written compressed under %q, got %q", compressedSuffix, suffix)
	}

	body, suffix = namePayloadFile(plain, nil, errors.New("encoder unavailable"), 7)
	if suffix != plainSuffix {
		t.Fatalf("a failed encode must take the plain name, got %q", suffix)
	}
	if string(body) != string(plain) {
		t.Fatalf("a failed encode must write the plain bytes, got %q", body)
	}
}

// The first stored payload is the one a finish may already have committed, so a second store keeps
// it, as Postgres does, and says so. The reader prefers the compressed name, so a file under the
// other setting counts as stored too.
func TestStoringAgainKeepsTheFirstPayloadUnderEitherSetting(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	if err := NewCompressingFileStorage(dir).Store(ctx, "esc", 7, 1, []byte(`"old"`), []byte(`"old"`)); err != nil {
		t.Fatalf("first store: %v", err)
	}
	for _, store := range []*FileStorage{NewFileStorage(dir), NewCompressingFileStorage(dir)} {
		if err := store.Store(ctx, "esc", 7, 1, []byte(`"new"`), []byte(`"new"`)); !errors.Is(err, ErrAlreadyStored) {
			t.Fatalf("second store: want ErrAlreadyStored, got %v", err)
		}
	}

	plainStore := NewFileStorage(dir)
	prompt, response, err := plainStore.Retrieve(ctx, "esc", 7, 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if string(prompt) != `"old"` || string(response) != `"old"` {
		t.Fatalf("the first payload was replaced: prompt=%s response=%s", prompt, response)
	}

	escrowDir, err := plainStore.escrowDir("esc", 1)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(escrowDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "7"+compressedSuffix {
		t.Fatalf("want only the first payload file, got %v", entries)
	}
}

// Of concurrent writers exactly one is stored, and the file is never a mix of two writes.
func TestConcurrentStoresKeepExactlyOnePayload(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store := NewFileStorage(dir)

	const writers = 16
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := []byte(fmt.Sprintf(`"writer %d %s"`, i, strings.Repeat("x", 1<<16)))
			errs[i] = store.Store(ctx, "esc", 9, 1, body, body)
		}()
	}
	wg.Wait()

	stored := 0
	for _, err := range errs {
		switch {
		case err == nil:
			stored++
		case !errors.Is(err, ErrAlreadyStored):
			t.Fatalf("store: %v", err)
		}
	}
	if stored != 1 {
		t.Fatalf("want exactly one stored payload, got %d", stored)
	}
	prompt, response, err := store.Retrieve(ctx, "esc", 9, 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if string(prompt) != string(response) {
		t.Fatalf("prompt and response come from different writes")
	}
	escrowDir, _ := store.escrowDir("esc", 1)
	entries, _ := os.ReadDir(escrowDir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

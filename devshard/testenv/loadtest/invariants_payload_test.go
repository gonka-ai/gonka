package loadtest

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"common/storage/payloads"

	"github.com/stretchr/testify/require"
)

// writePayloads stores n payloads through the devshardd file store, compressing as DEVSHARD_PAYLOAD_ZSTD_ENABLED would.
func writePayloads(t *testing.T, dir string, n int, compress bool) {
	t.Helper()
	store := payloads.NewFileStorage(dir)
	if compress {
		store = payloads.NewCompressingFileStorage(dir)
	}
	for i := 0; i < n; i++ {
		prompt := []byte(fmt.Sprintf(`{"messages":[{"content":"Summarise the main idea of this sentence briefly. %d","role":"user"}],"model":"model-a"}`, i))
		response := bytes.Repeat([]byte(`{"choices":[{"logprobs":{"content":[{"token":"1234","logprob":-0.1}]}}]}`), 8)
		require.NoError(t, store.Store(context.Background(), "11", uint64(i+1), 1, prompt, response))
	}
}

func payloadCheck(t *testing.T, stats PayloadFileStats) AssertionResult {
	t.Helper()
	in := passingInputs()
	in.PayloadFiles = map[string]PayloadFileStats{"versiond-4": stats}
	checks, _ := evaluateInvariants(in)
	found := checksNamed(t, checks, "payload-files-are-zstd.versiond-4.payload_files_zstd")
	require.Len(t, found, 1)
	return found[0]
}

// Test flow:
// 1. Store payloads with the compressing file store, with the plain one (the zstd-off control), too few, none and a corrupt file.
// 2. Scan each directory and evaluate payload-files-are-zstd for versiond-4.
// 3. Require a pass only for the compressing store, and a failure for every other directory and for a sampling error.
func TestInvariantPayloadFilesZstd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, dir string)
		passed bool
	}{
		{name: "zstd files", passed: true, setup: func(t *testing.T, dir string) { writePayloads(t, dir, payloadSampleMin, true) }},
		{name: "zstd disabled", passed: false, setup: func(t *testing.T, dir string) { writePayloads(t, dir, payloadSampleMin, false) }},
		{name: "too few files", passed: false, setup: func(t *testing.T, dir string) { writePayloads(t, dir, payloadSampleMin-1, true) }},
		{name: "empty dir", passed: false, setup: func(t *testing.T, dir string) {}},
		{name: "corrupt zstd file", passed: false, setup: func(t *testing.T, dir string) {
			writePayloads(t, dir, payloadSampleMin, true)
			path := filepath.Join(dir, "1", "11", "1.json.zst")
			require.NoError(t, os.WriteFile(path, append([]byte{0x28, 0xb5, 0x2f, 0xfd}, bytes.Repeat([]byte("x"), 64)...), 0o644))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			check := payloadCheck(t, scanPayloadDir(dir))
			require.Equal(t, tc.passed, check.Passed, "actual=%s details=%s", check.Actual, check.Details)
		})
	}
	require.False(t, payloadCheck(t, PayloadFileStats{Error: "docker exec failed"}).Passed)
}

// Test flow:
// 1. Tar a compressing store's directory the way the container probe does, plus an entry that escapes the target.
// 2. Extract the stream and scan the copy.
// 3. Require the escaping entry to be refused and the safe copy to scan like the source.
func TestExtractPayloadTar(t *testing.T) {
	src := t.TempDir()
	writePayloads(t, src, 3, true)
	var stream bytes.Buffer
	tw := tar.NewWriter(&stream)
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "./" + rel, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err = tw.Write(body)
		return err
	}))
	require.NoError(t, tw.Close())

	dst := t.TempDir()
	require.NoError(t, extractPayloadTar(bytes.NewReader(stream.Bytes()), dst))
	require.Equal(t, scanPayloadDir(src), scanPayloadDir(dst))

	var evil bytes.Buffer
	tw = tar.NewWriter(&evil)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../escape.json.zst", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.Error(t, extractPayloadTar(bytes.NewReader(evil.Bytes()), t.TempDir()))
}

package transport

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatGzipEmitter_WindowContinuity(t *testing.T) {
	var windowed bytes.Buffer
	e := NewChatGzipEmitter(func(chunk []byte) error {
		windowed.Write(chunk)
		return nil
	})
	line := bytes.Repeat([]byte("a"), 256)
	const n = 40
	for i := 0; i < n; i++ {
		_, err := e.Write(line)
		require.NoError(t, err)
		require.NoError(t, e.Flush())
	}
	require.NoError(t, e.Close())

	var independent int
	for i := 0; i < n; i++ {
		var buf bytes.Buffer
		gz, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		require.NoError(t, err)
		_, err = gz.Write(line)
		require.NoError(t, err)
		require.NoError(t, gz.Flush())
		require.NoError(t, gz.Close())
		independent += buf.Len()
	}
	require.Less(t, windowed.Len(), independent/2,
		"one gzip window across flushes must beat per-frame members (%d vs %d)", windowed.Len(), independent)
}

func TestChatGzipEmitter_NoDoubleCompression(t *testing.T) {
	var chunks [][]byte
	e := NewChatGzipEmitter(func(chunk []byte) error {
		chunks = append(chunks, append([]byte(nil), chunk...))
		return nil
	})
	_, err := e.Write([]byte("data: {\"devshard_receipt\":{}}\n\n"))
	require.NoError(t, err)
	require.NoError(t, e.Flush())
	_, err = e.Write([]byte("data: {\"choices\":[]}\n\n"))
	require.NoError(t, err)
	require.NoError(t, e.Flush())
	require.NoError(t, e.Close())
	require.GreaterOrEqual(t, len(chunks), 2)
	require.True(t, bytes.HasPrefix(chunks[0], []byte{0x1f, 0x8b}), "first chunk is the gzip member header")
	for i, chunk := range chunks[1:] {
		require.False(t, bytes.HasPrefix(chunk, []byte{0x1f, 0x8b}), "chunk %d must not be an independent gzip member", i+1)
	}
	decoded := gzipConcat(t, chunks)
	require.Contains(t, decoded, "devshard_receipt")
	require.False(t, bytes.HasPrefix([]byte(decoded), []byte{0x1f, 0x8b}))
}

func TestChatGzipEmitter_UnwrittenCloseEmitsNothing(t *testing.T) {
	var n int
	e := NewChatGzipEmitter(func([]byte) error {
		n++
		return errors.New("must not emit")
	})
	require.NoError(t, e.Close())
	require.Zero(t, n)
}

func TestChatFrameSink_SendErrorStopsLaterWrites(t *testing.T) {
	var n int
	sink := NewChatFrameSink(func([]byte) error {
		n++
		if n > 1 {
			return errors.New("peer gone")
		}
		return nil
	})
	_, err := sink.Write([]byte("data: a\n\n"))
	require.NoError(t, err)
	require.NoError(t, sink.FlushErr())
	_, err = sink.Write([]byte("data: b\n\n"))
	require.NoError(t, err)
	require.Error(t, sink.FlushErr())
	_, err = sink.Write([]byte("data: c\n\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "peer gone")
}

func TestChatFrameSink_FlushErrSurfacesSendError(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	_, err := sink.Write([]byte("data: a\n\n"))
	require.NoError(t, err)
	require.ErrorContains(t, sink.FlushErr(), "peer gone")
	_, err = sink.Write([]byte("data: b\n\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "peer gone")
}

func TestWriteSSEEvent_ChatFlushFailure(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	err := writeSSEEvent(sink, map[string]string{"devshard_receipt": "{}"})
	require.ErrorContains(t, err, "peer gone")
}

func TestReplaySSEBody_ChatFlushFailure(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	err := replaySSEBody(sink, []byte(`{"ok":true}`))
	require.ErrorContains(t, err, "peer gone")
}

func gzipConcat(t *testing.T, chunks [][]byte) string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(bytes.Join(chunks, nil)))
	require.NoError(t, err)
	defer gz.Close()
	decoded, err := io.ReadAll(gz)
	require.NoError(t, err)
	return string(decoded)
}

func TestChatGzipEmitter_ConcatIsOneStream(t *testing.T) {
	var chunks [][]byte
	e := NewChatGzipEmitter(func(chunk []byte) error {
		chunks = append(chunks, append([]byte(nil), chunk...))
		return nil
	})
	parts := []string{"one\n", "two\n", "three\n"}
	for _, p := range parts {
		_, err := e.Write([]byte(p))
		require.NoError(t, err)
		require.NoError(t, e.Flush())
	}
	require.NoError(t, e.Close())
	require.Equal(t, strings.Join(parts, ""), gzipConcat(t, chunks))
}

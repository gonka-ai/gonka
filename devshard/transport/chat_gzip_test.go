package transport

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
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

func TestChatFrameSink_NilClose(t *testing.T) {
	var sink *ChatFrameSink
	require.ErrorIs(t, sink.Close(), io.ErrClosedPipe)
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
	_, err := sink.Write([]byte(sseEventAtLeast(chatGzipCoalesceMin, "a")))
	require.NoError(t, err)
	require.NoError(t, sink.FlushErr())
	_, err = sink.Write([]byte(sseEventAtLeast(chatGzipCoalesceMin, "b")))
	require.NoError(t, err)
	require.Error(t, sink.FlushErr())
	_, err = sink.Write([]byte(sseEventAtLeast(chatGzipCoalesceMin, "c")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "peer gone")
}

func TestChatFrameSink_FlushErrSurfacesSendError(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	_, err := sink.Write([]byte(sseEventAtLeast(chatGzipCoalesceMin, "a")))
	require.NoError(t, err)
	require.ErrorContains(t, sink.FlushErr(), "peer gone")
	_, err = sink.Write([]byte(sseEventAtLeast(chatGzipCoalesceMin, "b")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "peer gone")
}

func TestWriteSSEEvent_ChatFlushFailure(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	err := writeSSEEvent(sink, map[string]string{"devshard_receipt": strings.Repeat("x", chatGzipCoalesceMin)})
	require.ErrorContains(t, err, "peer gone")
}

func TestReplaySSEBody_ChatFlushFailure(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	err := replaySSEBody(sink, bytes.Repeat([]byte("x"), chatGzipCoalesceMin))
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

func TestChatFrameSink_HoldsShortEventUntilClose(t *testing.T) {
	var chunks [][]byte
	sink := NewChatFrameSink(func(chunk []byte) error {
		chunks = append(chunks, append([]byte(nil), chunk...))
		return nil
	})
	event := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"
	require.Less(t, len(event), chatGzipCoalesceMin)
	require.NoError(t, relaySSELines(event, sink))
	require.Empty(t, chunks, "one token stays in the gzip window until the stream ends")
	require.NoError(t, sink.Close())
	require.Equal(t, event, gzipConcat(t, chunks))
}

func TestChatFrameSink_FlushesFinishedEventsAt128(t *testing.T) {
	var chunks [][]byte
	sink := NewChatFrameSink(func(chunk []byte) error {
		chunks = append(chunks, append([]byte(nil), chunk...))
		return nil
	})
	var body strings.Builder
	for {
		ev := "data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n"
		require.Less(t, len(ev), chatGzipCoalesceMin)
		body.WriteString(ev)
		require.NoError(t, relaySSELines(ev, sink))
		if body.Len() >= chatGzipCoalesceMin {
			break
		}
		require.Empty(t, chunks)
	}
	require.NotEmpty(t, chunks, "finished events leave once they reach the cut, before [DONE]")
	done := "data: [DONE]\n\n"
	require.NoError(t, relaySSELines(done, sink))
	require.NoError(t, sink.Close())
	require.Equal(t, body.String()+done, gzipConcat(t, chunks))
}

func TestChatFrameSink_FlushesAtCoalesceMaxWithoutEvent(t *testing.T) {
	var n int
	sink := NewChatFrameSink(func([]byte) error {
		n++
		return nil
	})
	_, err := sink.Write(bytes.Repeat([]byte("a"), chatGzipCoalesceMax))
	require.NoError(t, err)
	require.Equal(t, 1, n, "a line with no blank line still leaves at 16 KiB")
}

func TestChatFrameSink_CoalesceBeatsPerLineFlush(t *testing.T) {
	body := sampleChatSSE(80)
	perLine := gzipRelay(t, body, true)
	perEvent := gzipPerEvent(t, body)
	coalesced := gzipRelay(t, body, false)
	oneShot := gzipOneShot(t, body)
	require.Equal(t, body, gzipConcat(t, perLine.chunks))
	require.Equal(t, body, gzipConcat(t, perEvent.chunks))
	require.Equal(t, body, gzipConcat(t, coalesced.chunks))
	require.Equal(t, body, gzipConcat(t, oneShot.chunks))
	require.Less(t, coalesced.bytes, perLine.bytes)
	require.Less(t, coalesced.bytes, perEvent.bytes)
	wonLine := 100 * (perLine.bytes - coalesced.bytes) / perLine.bytes
	wonEvent := 100 * (perEvent.bytes - coalesced.bytes) / perEvent.bytes
	t.Logf("uncompressed %d; per-line %d bytes / %d frames; per-event %d bytes / %d frames; coalesced %d bytes / %d frames; one-shot %d bytes; won %d%% vs per-line, %d%% vs per-event",
		len(body), perLine.bytes, len(perLine.chunks), perEvent.bytes, len(perEvent.chunks), coalesced.bytes, len(coalesced.chunks), oneShot.bytes, wonLine, wonEvent)
	require.GreaterOrEqual(t, wonLine, 40, "coalesced gzip must keep the match table, not only skip the blank line")
}

func gzipOneShot(t *testing.T, body string) gzipMeasure {
	t.Helper()
	var m gzipMeasure
	e := NewChatGzipEmitter(func(chunk []byte) error {
		m.bytes += len(chunk)
		m.chunks = append(m.chunks, append([]byte(nil), chunk...))
		return nil
	})
	_, err := e.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, e.Close())
	return m
}

func TestChatFrameSink_ReceiptFlushNowSurfacesSendError(t *testing.T) {
	sink := NewChatFrameSink(func([]byte) error {
		return errors.New("peer gone")
	})
	err := writeSSEEvent(sink, map[string]string{"devshard_receipt": "{}"})
	require.NoError(t, err, "a receipt under the cut stays buffered")
	require.ErrorContains(t, flushSSENow(sink), "peer gone")
}

func TestChatFrameSink_GatewayUnpacksCoalescedStream(t *testing.T) {
	var chunks [][]byte
	sink := NewChatFrameSink(func(chunk []byte) error {
		chunks = append(chunks, append([]byte(nil), chunk...))
		return nil
	})
	var body strings.Builder
	events := 0
	for events < 12 {
		events++
		ev := vllmTokenEvent(events, []string{"Hello", " world", " from", " the", " model"}[(events-1)%5])
		body.WriteString(ev)
		require.NoError(t, relaySSELines(ev, sink))
		if len(chunks) > 0 {
			break
		}
	}
	require.Equal(t, 5, events, "1024 bytes is about five vLLM token events")
	for events < 12 {
		events++
		ev := vllmTokenEvent(events, []string{"Hello", " world", " from", " the", " model"}[(events-1)%5])
		body.WriteString(ev)
		require.NoError(t, relaySSELines(ev, sink))
	}
	body.WriteString("data: [DONE]\n\n")
	require.NoError(t, relaySSELines("data: [DONE]\n\n", sink))
	require.NoError(t, sink.Close())
	require.Equal(t, body.String(), gzipConcat(t, chunks))

	client := &HTTPClient{}
	fromFrames := gatewayStream(t, client, &chunkReader{chunks: chunks})
	var fromRaw bytes.Buffer
	_, err := client.parseSSEResponse(context.Background(), strings.NewReader(body.String()), &fromRaw, nil)
	require.NoError(t, err)
	require.Equal(t, fromRaw.String(), fromFrames)
	require.Contains(t, fromFrames, "data: [DONE]\n\n")
	require.Contains(t, fromFrames, `"content":"Hello"`)
	require.Contains(t, fromFrames, `"content":" model"`)
}

func vllmTokenEvent(i int, content string) string {
	return fmt.Sprintf("data: {\"id\":\"chatcmpl-%08d\",\"object\":\"chat.completion.chunk\",\"created\":1730000000,\"model\":\"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", i, content)
}

func gatewayStream(t *testing.T, client *HTTPClient, r io.Reader) string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	require.NoError(t, err)
	defer gz.Close()
	var forwarded bytes.Buffer
	_, err = client.parseSSEResponse(context.Background(), gz, &forwarded, nil)
	require.NoError(t, err)
	return forwarded.String()
}

type chunkReader struct {
	chunks [][]byte
	i, off int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i][r.off:])
	r.off += n
	if r.off >= len(r.chunks[r.i]) {
		r.i++
		r.off = 0
	}
	return n, nil
}

func sseEventAtLeast(min int, content string) string {
	ev := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
	if len(ev) >= min {
		return ev
	}
	pad := strings.Repeat("x", min)
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q,\"pad\":%q}}]}\n\n", content, pad)
}

func sampleChatSSE(tokens int) string {
	sig := strings.Repeat("A", 88)
	hash := strings.Repeat("B", 44)
	var b strings.Builder
	fmt.Fprintf(&b, "data: {\"devshard_receipt\":{\"state_sig\":%q,\"state_hash\":%q,\"nonce\":1,\"receipt\":%q,\"confirmed_at\":1710000000}}\n\n", sig, hash, sig)
	words := []string{"Hello", " world", " from", " the", " model"}
	for i := 0; i < tokens; i++ {
		fmt.Fprintf(&b, "data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", words[i%len(words)])
	}
	b.WriteString("data: [DONE]\n\n")
	b.WriteString("data: {\"devshard_meta\":{}}\n\n")
	return b.String()
}

type gzipMeasure struct {
	bytes  int
	chunks [][]byte
}

func gzipRelay(t *testing.T, body string, perLine bool) gzipMeasure {
	t.Helper()
	var m gzipMeasure
	send := func(chunk []byte) error {
		m.bytes += len(chunk)
		m.chunks = append(m.chunks, append([]byte(nil), chunk...))
		return nil
	}
	if perLine {
		e := NewChatGzipEmitter(send)
		sc := bufio.NewScanner(strings.NewReader(body))
		for sc.Scan() {
			_, err := fmt.Fprintln(e, sc.Text())
			require.NoError(t, err)
			require.NoError(t, e.Flush())
		}
		require.NoError(t, sc.Err())
		require.NoError(t, e.Close())
		return m
	}
	sink := NewChatFrameSink(send)
	require.NoError(t, relaySSELines(body, sink))
	require.NoError(t, sink.Close())
	return m
}

func gzipPerEvent(t *testing.T, body string) gzipMeasure {
	t.Helper()
	var m gzipMeasure
	e := NewChatGzipEmitter(func(chunk []byte) error {
		m.bytes += len(chunk)
		m.chunks = append(m.chunks, append([]byte(nil), chunk...))
		return nil
	})
	var tail [2]byte
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text() + "\n"
		_, err := e.Write([]byte(line))
		require.NoError(t, err)
		if len(line) == 1 {
			tail[0], tail[1] = tail[1], line[0]
		} else {
			tail[0], tail[1] = line[len(line)-2], line[len(line)-1]
		}
		if tail[0] == '\n' && tail[1] == '\n' {
			require.NoError(t, e.Flush())
		}
	}
	require.NoError(t, sc.Err())
	require.NoError(t, e.Close())
	return m
}

func relaySSELines(body string, w io.Writer) error {
	sink, _ := w.(interface{ FlushErr() error })
	flusher, _ := w.(interface{ Flush() })
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		if _, err := fmt.Fprintln(w, sc.Text()); err != nil {
			return err
		}
		if sink != nil {
			if err := sink.FlushErr(); err != nil {
				return err
			}
			continue
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return sc.Err()
}

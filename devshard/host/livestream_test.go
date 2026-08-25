package host

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// newSpooledLiveStream builds a stream with the disk resume tier enabled, which
// is the production shape: without a spool there is no resume tier at all and
// Subscribe is refused outright.
func newSpooledLiveStream(t *testing.T) *LiveStream {
	t.Helper()
	s := newLiveStream()
	require.NoError(t, s.enableSpool(t.TempDir(), "escrow-1", 1))
	t.Cleanup(func() {
		s.Close(nil)
		s.Release()
	})
	return s
}

// waitSpooled blocks until the pump has written at least want bytes to disk.
func waitSpooled(t *testing.T, s *LiveStream, want int64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for s.SpooledBytes() < want {
		select {
		case <-deadline:
			t.Fatalf("spool stuck at %d bytes, wanted %d", s.SpooledBytes(), want)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

func waitRetainedAtMost(t *testing.T, s *LiveStream, want int64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for s.RetainedBytes() > want {
		select {
		case <-deadline:
			t.Fatalf("retained %d bytes, wanted at most %d", s.RetainedBytes(), want)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// flushRecorder is a ResponseRecorder that may be written by a subscriber
// goroutine while the test inspects the body, so both sides take mu.
type flushRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushRecorder) Flush() {}

func (f *flushRecorder) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResponseRecorder.Write(p)
}

func (f *flushRecorder) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResponseRecorder.Body.String()
}

func (f *flushRecorder) bodyLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResponseRecorder.Body.Len()
}

// stripDevshardTSComments drops Step 5g writer-only hop comments so tests can
// assert on the cursor-counted log bytes independently of timestamp injection.
func stripDevshardTSComments(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(line, DevshardTSCommentPrefix) || strings.HasPrefix(strings.TrimSuffix(line, "\n"), strings.TrimSpace(DevshardTSCommentPrefix)) {
			continue
		}
		// SplitAfter keeps the newline on each piece; a bare ": devshard-ts …\n"
		// is skipped above. Also skip when the prefix matches without relying on
		// SplitAfter edge cases.
		trimmed := strings.TrimSuffix(line, "\n")
		trimmed = strings.TrimSuffix(trimmed, "\r")
		if strings.HasPrefix(trimmed, ": devshard-ts") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

func waitBodyContains(t *testing.T, get func() string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if bytes.Contains([]byte(get()), []byte(want)) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %q, got %q", want, get())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestLiveStream_SubscribePartialThenLive(t *testing.T) {
	stream := newSpooledLiveStream(t)
	event1 := []byte(`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n")
	event2 := []byte(`data: {"choices":[{"delta":{"content":"lo"}}]}` + "\n")

	_, err := stream.Write(event1)
	require.NoError(t, err)

	// Producer has written a prefix of event2; client already saw some of it.
	partial := event2[:10]
	clientHad := 4
	restOfPartial := partial[clientHad:]
	rest := event2[10:]
	_, err = stream.Write(partial)
	require.NoError(t, err)
	require.Equal(t, 1, stream.EventCount())
	require.Equal(t, len(partial), stream.FormingLen())

	rec := newFlushRecorder()
	done := make(chan error, 1)
	go func() {
		done <- stream.Subscribe(rec, 1, int64(clientHad))
	}()

	waitBodyContains(t, rec.body, string(restOfPartial), 500*time.Millisecond)

	_, err = stream.Write(rest)
	require.NoError(t, err)
	_, err = stream.Write([]byte(`data: [DONE]` + "\n"))
	require.NoError(t, err)
	stream.Close(nil)

	require.NoError(t, <-done)
	body := []byte(rec.body())
	require.True(t, bytes.Contains(body, restOfPartial))
	require.True(t, bytes.Contains(body, rest))
	require.True(t, bytes.Contains(body, []byte("[DONE]")))
	require.False(t, bytes.Contains(body, []byte(`"Hel"`)), "already-delivered event1 must not be re-sent")
	require.Equal(t, 1, bytes.Count(body, restOfPartial), "partial remainder must not be duplicated")
	require.Equal(t, 1, bytes.Count(body, rest), "live tail must not be duplicated")
}

// A reader must never rescan the log from event 0, yet must still pick up bytes
// appended to the last event after it was already consumed (bufferLocked merges
// blank SSE separators into it).
func TestLiveStream_CopyPendingAdvancesCursorWithoutLosingBytes(t *testing.T) {
	stream := newLiveStream()
	sub := &liveSubscriber{}
	var want, got []byte

	readPending := func() {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		got = append(got, stream.copyPendingLocked(sub, 0)...)
		sub.offset = stream.totalBytes
	}

	for i := 0; i < 50; i++ {
		content := []byte(fmt.Sprintf("data: chunk-%d\n", i))
		want = append(want, content...)
		_, err := stream.Write(content)
		require.NoError(t, err)
		readPending()

		// Separator arrives after the content line was already delivered.
		want = append(want, '\n')
		_, err = stream.Write([]byte("\n"))
		require.NoError(t, err)
		readPending()
	}

	require.Equal(t, string(want), string(got), "every produced byte delivered exactly once")
	require.Equal(t, 50, stream.EventCount())
	require.Equal(t, stream.EventCount()-1, sub.evIdx,
		"cursor must track forward instead of rescanning from event 0")
}

// copyPendingLocked must respect its byte limit so one wakeup cannot turn a
// large backlog into a single unbounded write.
func TestLiveStream_CopyPendingRespectsChunkLimit(t *testing.T) {
	stream := newLiveStream()
	for i := 0; i < 20; i++ {
		_, err := stream.Write([]byte(fmt.Sprintf("data: chunk-%02d\n", i)))
		require.NoError(t, err)
	}
	sub := &liveSubscriber{}
	var got []byte
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for sub.offset < stream.totalBytes {
		chunk := stream.copyPendingLocked(sub, 16)
		require.NotEmpty(t, chunk)
		require.LessOrEqual(t, len(chunk), 16)
		got = append(got, chunk...)
		sub.offset += int64(len(chunk))
	}
	require.Equal(t, stream.totalBytes, int64(len(got)))
}

func TestLiveStream_CursorPastBuffer(t *testing.T) {
	stream := newSpooledLiveStream(t)
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	err = stream.Subscribe(rec, 5, 0)
	require.ErrorIs(t, err, ErrResumeCursorPast)
}

// Real SSE framing is "data: …\n\n". Gateway delivered_events counts only
// non-blank lines; host must use the same space or reconnect duplicates content.
func TestLiveStream_CursorMatchesGatewayDoubleNewlineFraming(t *testing.T) {
	stream := newSpooledLiveStream(t)
	for _, ev := range []string{"data: A", "data: B", "data: C"} {
		_, err := stream.Write([]byte(ev + "\n\n"))
		require.NoError(t, err)
	}
	require.Equal(t, 3, stream.EventCount(), "blank separators must not be separate host events")

	rec := newFlushRecorder()
	done := make(chan error, 1)
	go func() {
		// Gateway cursor after forwarding A,B,C (each with trailing blank).
		done <- stream.Subscribe(rec, 3, 0)
	}()
	stream.Close(nil)
	require.NoError(t, <-done)

	require.Empty(t, rec.body(), "cursor(3,0) must resume past all delivered content")
}

func TestLiveStream_ReconnectAfterTwoOfThreeDoubleNewlineEvents(t *testing.T) {
	stream := newSpooledLiveStream(t)
	for _, ev := range []string{"data: A", "data: B", "data: C"} {
		_, err := stream.Write([]byte(ev + "\n\n"))
		require.NoError(t, err)
	}

	rec := newFlushRecorder()
	done := make(chan error, 1)
	go func() {
		done <- stream.Subscribe(rec, 2, 0)
	}()
	stream.Close(nil)
	require.NoError(t, <-done)

	got := rec.body()
	require.Contains(t, got, "data: C")
	require.NotContains(t, got, "data: A")
	require.NotContains(t, got, "data: B")
	require.Equal(t, 1, bytes.Count([]byte(got), []byte("data: C")))
}

func TestLiveStream_NegativeCursorRejected(t *testing.T) {
	stream := newSpooledLiveStream(t)
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	require.ErrorIs(t, stream.Subscribe(rec, -1, 0), ErrInvalidResumeCursor)
	require.ErrorIs(t, stream.Subscribe(rec, 0, -1), ErrInvalidResumeCursor)

	// Panic must not poison the mutex: producer Write must still complete.
	produced := make(chan struct{})
	go func() {
		_, _ = stream.Write([]byte("data: two\n"))
		close(produced)
	}()
	select {
	case <-produced:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("LiveStream.mu poisoned after negative cursor")
	}
}

// gateWriter blocks inside Write until release is closed or its write deadline
// passes. Real gateway connections implement SetWriteDeadline through
// http.ResponseController; modelling that is what lets these tests exercise the
// stall paths without the drain goroutine abandoning an in-flight write.
type gateWriter struct {
	onFirstWrite chan struct{}
	release      chan struct{}
	mu           sync.Mutex
	buf          bytes.Buffer
	deadline     time.Time
}

func newGateWriter() *gateWriter {
	return &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (g *gateWriter) Header() http.Header { return make(http.Header) }
func (g *gateWriter) WriteHeader(int)     {}
func (g *gateWriter) Flush()              {}

func (g *gateWriter) SetWriteDeadline(t time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deadline = t
	return nil
}

func (g *gateWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	_, _ = g.buf.Write(p)
	deadline := g.deadline
	g.mu.Unlock()
	select {
	case <-g.onFirstWrite:
	default:
		close(g.onFirstWrite)
	}
	var expired <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-g.release:
		return len(p), nil
	case <-expired:
		return 0, errors.New("write deadline exceeded")
	}
}

func (g *gateWriter) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.String()
}

func TestLiveStream_ReplayDoesNotHoldLockDuringClientWrite(t *testing.T) {
	stream := newSpooledLiveStream(t)
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	_, err = stream.Write([]byte("data: two\n"))
	require.NoError(t, err)

	w := newGateWriter()
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()

	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("subscribe never started writing replay")
	}

	produced := make(chan struct{})
	go func() {
		_, _ = stream.Write([]byte("data: three\n"))
		close(produced)
	}()
	select {
	case <-produced:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ML producer blocked while reconnect client Write was slow")
	}

	close(w.release)
	stream.Close(nil)
	require.NoError(t, <-subDone)
	require.Contains(t, w.String(), "data: one")
	require.Contains(t, w.String(), "data: three")
}

func TestLiveStream_SlowSubscriberDoesNotDrop(t *testing.T) {
	stream := newSpooledLiveStream(t)
	w := newGateWriter()
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()
	time.Sleep(20 * time.Millisecond)

	_, err := stream.Write([]byte("data: A\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("subscriber never started writing")
	}

	// Client Write is blocked; keep producing — bytes must be retained.
	const extra = 100
	for i := 0; i < extra; i++ {
		_, err := stream.Write([]byte("data: B\n"))
		require.NoError(t, err)
	}

	close(w.release)
	stream.Close(nil)
	require.NoError(t, <-subDone)

	got := w.String()
	require.Equal(t, 1, bytes.Count([]byte(got), []byte("data: A\n")))
	require.Equal(t, extra, bytes.Count([]byte(got), []byte("data: B\n")),
		"slow subscriber must not silently lose producer bytes")
}

func TestLiveStream_PruneTTL(t *testing.T) {
	require.Equal(t, types.DefaultExecutionTimeout(), InflightReplayBufferTTL,
		"must derive from protocol ExecutionTimeout (same budget as gateway hard timeout)")
	stream := newSpooledLiveStream(t)
	stream.createdAt = time.Now().Add(-InflightReplayBufferTTL - time.Second)
	rec := httptest.NewRecorder()
	err := stream.Subscribe(rec, 0, 0)
	require.ErrorIs(t, err, ErrLiveStreamPruned)
}

func TestPruneLiveStreamLocked_DetachesWithoutClose(t *testing.T) {
	h := &Host{liveStreams: make(map[uint64]*LiveStream)}
	stream := newLiveStream()
	stream.createdAt = time.Now().Add(-InflightReplayBufferTTL - time.Second)
	stream.SetPrimary(httptest.NewRecorder())
	h.liveStreams[1] = stream

	h.mu.Lock()
	h.pruneLiveStreamLocked(1)
	h.mu.Unlock()

	require.Nil(t, h.liveStreams[1])
	require.ErrorIs(t, h.AttachLiveStream(1, httptest.NewRecorder(), 0, 0), ErrLiveStreamGone)

	// Producer must keep buffering after map detach (no Close).
	_, err := stream.Write([]byte("data: still-alive\n"))
	require.NoError(t, err)
	require.Equal(t, 1, stream.EventCount())
}

func TestLiveStream_PrimaryAndSubscriberFanout(t *testing.T) {
	stream := newSpooledLiveStream(t)
	primary := newFlushRecorder()
	stream.SetPrimary(primary)

	subRec := newFlushRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = stream.Subscribe(subRec, 0, 0)
	}()

	time.Sleep(20 * time.Millisecond)
	_, err := stream.Write([]byte("data: hi\n"))
	require.NoError(t, err)
	stream.Close(nil)
	wg.Wait()

	waitBodyContains(t, primary.body, "data: hi", time.Second)
	require.Contains(t, subRec.body(), "data: hi")
}

func TestLiveStream_WaitPrimaryFencesTrailingWrites(t *testing.T) {
	stream := newSpooledLiveStream(t)
	w := newGateWriter()
	stream.SetPrimary(w)

	_, err := stream.Write([]byte("data: body\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("primary never started writing")
	}

	stream.Close(nil)

	waited := make(chan struct{})
	go func() {
		stream.WaitPrimary()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("WaitPrimary returned while the primary reader still owned the writer")
	case <-time.After(50 * time.Millisecond):
	}

	close(w.release)
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("WaitPrimary never returned after the primary drained")
	}
	// Only now may the caller append a trailer (devshard_meta) to the writer.
	require.Contains(t, w.String(), "data: body")
}

// fenceWriter only records bytes it actually accepted, and fails the write when
// its deadline passes. That makes "did a body byte land after the fence?"
// observable: a drain that abandoned an in-flight write to a helper goroutine
// could still deliver body bytes after WaitPrimary released the writer.
type fenceWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	deadline time.Time
	started  chan struct{}
}

func newFenceWriter() *fenceWriter {
	return &fenceWriter{started: make(chan struct{})}
}

func (w *fenceWriter) Header() http.Header { return make(http.Header) }
func (w *fenceWriter) WriteHeader(int)     {}
func (w *fenceWriter) Flush()              {}

func (w *fenceWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

func (w *fenceWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	if deadline.IsZero() {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.buf.Write(p)
	}
	// Black-holed socket: never accepts, only times out.
	time.Sleep(time.Until(deadline))
	return 0, errors.New("write deadline exceeded")
}

func (w *fenceWriter) appendTrailer(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = time.Time{}
	w.buf.WriteString(s)
}

func (w *fenceWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestLiveStream_WriteDeadlineDoesNotOutliveWaitPrimary(t *testing.T) {
	prev := LiveStreamPrimaryWriteTimeout
	LiveStreamPrimaryWriteTimeout = 40 * time.Millisecond
	t.Cleanup(func() { LiveStreamPrimaryWriteTimeout = prev })

	stream := newSpooledLiveStream(t)
	w := newFenceWriter()
	stream.SetPrimary(w)
	_, err := stream.Write([]byte("data: body\n"))
	require.NoError(t, err)

	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("primary never entered Write")
	}

	stream.Close(nil)
	stream.WaitPrimary()

	// The fence has been released; the owner of the response may now write.
	w.appendTrailer("TRAILER")
	time.Sleep(100 * time.Millisecond) // any abandoned write would land here
	require.Equal(t, "TRAILER", w.String(),
		"no body byte may reach the writer after WaitPrimary returned")
	require.True(t, stream.ClientDetached())
}

func TestLiveStream_ReconnectStallReturnsLagged(t *testing.T) {
	prevStall := LiveStreamReaderStallTimeout
	prevWrite := LiveStreamPrimaryWriteTimeout
	LiveStreamReaderStallTimeout = 40 * time.Millisecond
	LiveStreamPrimaryWriteTimeout = time.Hour // stall clock should win for reconnect
	t.Cleanup(func() {
		LiveStreamReaderStallTimeout = prevStall
		LiveStreamPrimaryWriteTimeout = prevWrite
	})

	stream := newSpooledLiveStream(t)
	w := newGateWriter() // never released
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()
	_, err := stream.Write([]byte("data: stuck\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("reconnect never entered Write")
	}

	select {
	case err := <-subDone:
		require.ErrorIs(t, err, ErrSubscriberLagged)
	case <-time.After(time.Second):
		t.Fatal("expected ErrSubscriberLagged")
	}
	require.False(t, stream.ClientDetached())
}

func TestLiveStream_PrimaryStallIsClientDetachedNotLagged(t *testing.T) {
	prevStall := LiveStreamReaderStallTimeout
	prevWrite := LiveStreamPrimaryWriteTimeout
	LiveStreamReaderStallTimeout = 40 * time.Millisecond
	LiveStreamPrimaryWriteTimeout = 40 * time.Millisecond
	t.Cleanup(func() {
		LiveStreamReaderStallTimeout = prevStall
		LiveStreamPrimaryWriteTimeout = prevWrite
	})

	stream := newSpooledLiveStream(t)
	w := newGateWriter() // never released
	stream.SetPrimary(w)
	_, err := stream.Write([]byte("data: stuck\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("primary never entered Write")
	}

	deadline := time.After(time.Second)
	for !stream.ClientDetached() {
		select {
		case <-deadline:
			t.Fatal("primary write deadline did not detach")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Producer continues after primary detach.
	_, err = stream.Write([]byte("data: after\n"))
	require.NoError(t, err)
	require.Equal(t, 2, stream.EventCount())
	stream.Close(nil)
	stream.WaitPrimary()
}

func TestLiveStream_SlowButAliveReconnectCompletes(t *testing.T) {
	prevStall := LiveStreamReaderStallTimeout
	prevRing := LiveStreamRingBytes
	LiveStreamReaderStallTimeout = time.Second
	LiveStreamRingBytes = 64
	t.Cleanup(func() {
		LiveStreamReaderStallTimeout = prevStall
		LiveStreamRingBytes = prevRing
	})

	stream := newSpooledLiveStream(t)
	w := &slowWriter{delay: 15 * time.Millisecond}
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()

	var want strings.Builder
	for i := 0; i < 8; i++ {
		ev := fmt.Sprintf("data: chunk-%d-%s\n", i, strings.Repeat("x", 20))
		want.WriteString(ev)
		_, err := stream.Write([]byte(ev))
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
	}
	stream.Close(nil)

	select {
	case err := <-subDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("slow-but-alive subscribe did not finish")
	}
	require.Equal(t, want.String(), stripDevshardTSComments(w.String()),
		"a reader that falls out of the RAM window must be served from the spool without gaps")
}

func TestLiveStream_CaughtUpIdleWaitNotStalled(t *testing.T) {
	prevStall := LiveStreamReaderStallTimeout
	LiveStreamReaderStallTimeout = 30 * time.Millisecond
	t.Cleanup(func() { LiveStreamReaderStallTimeout = prevStall })

	stream := newSpooledLiveStream(t)
	rec := newFlushRecorder()
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(rec, 0, 0)
	}()

	time.Sleep(80 * time.Millisecond) // longer than stall timeout while caught up
	_, err := stream.Write([]byte("data: late\n"))
	require.NoError(t, err)
	stream.Close(nil)

	select {
	case err := <-subDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("caught-up idle sub was closed or hung")
	}
	require.Contains(t, rec.body(), "data: late")
}

// The headline property of the spool design: a reader that has stopped
// consuming no longer pins the RAM window, and still loses no bytes.
func TestLiveStream_RAMBoundedWhileReaderIsPinned(t *testing.T) {
	prevRing := LiveStreamRingBytes
	prevStall := LiveStreamReaderStallTimeout
	LiveStreamRingBytes = 256
	LiveStreamReaderStallTimeout = 10 * time.Second
	t.Cleanup(func() {
		LiveStreamRingBytes = prevRing
		LiveStreamReaderStallTimeout = prevStall
	})

	stream := newSpooledLiveStream(t)
	w := newGateWriter()
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()

	var want strings.Builder
	ev := "data: " + strings.Repeat("q", 40) + "\n"
	want.WriteString(ev)
	_, err := stream.Write([]byte(ev))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("reader never started writing")
	}

	// Reader is parked at offset 0 for the whole burst.
	for i := 0; i < 200; i++ {
		ev := fmt.Sprintf("data: %03d-%s\n", i, strings.Repeat("q", 40))
		want.WriteString(ev)
		_, err := stream.Write([]byte(ev))
		require.NoError(t, err)
	}
	require.Greater(t, stream.TotalBytes(), int64(8000))

	waitRetainedAtMost(t, stream, LiveStreamRingBytes)
	require.Greater(t, stream.BytesBase(), int64(0),
		"head-trim must advance past a pinned reader once the bytes are spooled")

	close(w.release)
	stream.Close(nil)
	require.NoError(t, <-subDone)
	require.Equal(t, want.String(), stripDevshardTSComments(w.String()),
		"pinned reader must still receive every byte, from the spool")
}

// A reconnect whose cursor is far older than the RAM window resolves through
// the on-disk event index instead of failing with ErrResumeCursorPast.
func TestLiveStream_ResumeFromCursorOlderThanRAMWindow(t *testing.T) {
	prevRing := LiveStreamRingBytes
	LiveStreamRingBytes = 128
	t.Cleanup(func() { LiveStreamRingBytes = prevRing })

	stream := newSpooledLiveStream(t)
	var events []string
	for i := 0; i < 60; i++ {
		ev := fmt.Sprintf("data: e%02d-%s\n\n", i, strings.Repeat("z", 30))
		events = append(events, ev)
		_, err := stream.Write([]byte(ev))
		require.NoError(t, err)
	}
	waitRetainedAtMost(t, stream, LiveStreamRingBytes+int64(len(events[0])))
	require.Greater(t, stream.eventsBaseOrZero(), int64(4), "cursor under test must be outside the window")

	stream.Close(nil)

	for _, cursor := range []int64{0, 3, 17} {
		rec := newFlushRecorder()
		require.NoError(t, stream.Subscribe(rec, cursor, 0), "cursor %d", cursor)
		require.Equal(t, strings.Join(events[cursor:], ""), rec.body(),
			"resume from event %d must be byte-identical to the untrimmed tail", cursor)
	}

	// Partial inside a trimmed event resolves through the index too.
	rec := newFlushRecorder()
	require.NoError(t, stream.Subscribe(rec, 5, 6))
	require.Equal(t, events[5][6:]+strings.Join(events[6:], ""), rec.body())
}

func TestLiveStream_SpoolCursorPastTipRejected(t *testing.T) {
	stream := newSpooledLiveStream(t)
	_, err := stream.Write([]byte("data: one\n\n"))
	require.NoError(t, err)
	waitSpooled(t, stream, 1)

	rec := httptest.NewRecorder()
	require.ErrorIs(t, stream.Subscribe(rec, 0, 999), ErrResumeCursorPast)
}

func TestLiveStream_NoSpoolRefusesSubscribe(t *testing.T) {
	stream := newLiveStream()
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	require.False(t, stream.SpoolActive())

	require.ErrorIs(t, stream.Subscribe(httptest.NewRecorder(), 0, 0), ErrLiveStreamResumeUnavailable)
}

// Without a spool nothing can be reclaimed from disk, so a reader that stops
// consuming is dropped at the hard ceiling rather than growing RAM.
func TestLiveStream_NoSpoolDropsPinnedReaderAtCeiling(t *testing.T) {
	prevRing := LiveStreamRingBytes
	prevMax := LiveStreamMaxRAMBytes
	LiveStreamRingBytes = 64
	LiveStreamMaxRAMBytes = 256
	t.Cleanup(func() {
		LiveStreamRingBytes = prevRing
		LiveStreamMaxRAMBytes = prevMax
	})

	stream := newLiveStream()
	w := newGateWriter() // never released
	stream.SetPrimary(w)

	_, err := stream.Write([]byte("data: head\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("primary never started writing")
	}
	for i := 0; i < 40; i++ {
		_, err := stream.Write([]byte(fmt.Sprintf("data: t%02d-%s\n", i, strings.Repeat("y", 20))))
		require.NoError(t, err)
	}

	require.LessOrEqual(t, stream.RetainedBytes(), LiveStreamMaxRAMBytes,
		"degraded mode must still respect the hard RAM ceiling")
	require.True(t, stream.ClientDetached(), "pinned primary must be dropped to reclaim the window")
	stream.Close(nil)
}

func TestLiveStream_ReleaseRemovesSpoolFiles(t *testing.T) {
	dir := t.TempDir()
	stream := newLiveStream()
	require.NoError(t, stream.enableSpool(dir, "escrow-1", 7))
	_, err := stream.Write([]byte("data: one\n\n"))
	require.NoError(t, err)
	waitSpooled(t, stream, 1)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "expected .log and .idx")

	stream.Close(nil)
	stream.Release()

	deadline := time.After(2 * time.Second)
	for {
		entries, err = os.ReadDir(dir)
		require.NoError(t, err)
		if len(entries) == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("spool files not removed: %v", entries)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestPrepareLiveStreamSpoolDir_ClearsLeftovers(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/stale.log", []byte("junk"), 0o600))
	require.NoError(t, PrepareLiveStreamSpoolDir(dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// A far-behind reconnect must be written in bounded chunks: one giant write
// racing a single deadline is what turned a slow-but-healthy link into
// ErrSubscriberLagged.
func TestLiveStream_BacklogIsWrittenInChunks(t *testing.T) {
	prevChunk := LiveStreamWriteChunkBytes
	LiveStreamWriteChunkBytes = 64
	t.Cleanup(func() { LiveStreamWriteChunkBytes = prevChunk })

	stream := newSpooledLiveStream(t)
	var want strings.Builder
	for i := 0; i < 40; i++ {
		ev := fmt.Sprintf("data: b%02d-%s\n", i, strings.Repeat("w", 20))
		want.WriteString(ev)
		_, err := stream.Write([]byte(ev))
		require.NoError(t, err)
	}
	stream.Close(nil)

	w := &countingWriter{}
	require.NoError(t, stream.Subscribe(w, 0, 0))
	require.Equal(t, want.String(), stripDevshardTSComments(w.String()))
	require.Greater(t, w.writes(), 1, "backlog must not be one write")
	require.LessOrEqual(t, w.maxDataWrite(), 64, "data chunks must respect LiveStreamWriteChunkBytes; hop comments are separate")
}

type countingWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	n        int
	maxSz    int
	maxData  int
}

func (w *countingWriter) Header() http.Header { return make(http.Header) }
func (w *countingWriter) WriteHeader(int)     {}
func (w *countingWriter) Flush()              {}
func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
	if len(p) > w.maxSz {
		w.maxSz = len(p)
	}
	if !bytes.HasPrefix(p, []byte(DevshardTSCommentPrefix)) && !bytes.HasPrefix(p, []byte(": devshard-ts")) {
		if len(p) > w.maxData {
			w.maxData = len(p)
		}
	}
	return w.buf.Write(p)
}
func (w *countingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
func (w *countingWriter) writes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}
func (w *countingWriter) maxWrite() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxSz
}
func (w *countingWriter) maxDataWrite() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxData
}

func (s *LiveStream) eventsBaseOrZero() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventsBase
}

func (s *LiveStream) subscriberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func TestLiveStream_FormingOversizeClosesStream(t *testing.T) {
	prev := LiveStreamMaxFormingBytes
	LiveStreamMaxFormingBytes = 32
	t.Cleanup(func() { LiveStreamMaxFormingBytes = prev })

	stream := newSpooledLiveStream(t)
	_, err := stream.Write([]byte("data: " + strings.Repeat("z", 64))) // no newline
	require.NoError(t, err)
	require.Greater(t, int64(stream.FormingLen()), LiveStreamMaxFormingBytes)
	require.ErrorIs(t, stream.Subscribe(httptest.NewRecorder(), 0, 0), ErrLiveStreamFormingOversize)

	// Further writes must not grow forming without bound.
	before := stream.FormingLen()
	_, err = stream.Write([]byte(strings.Repeat("z", 1024)))
	require.NoError(t, err)
	require.Equal(t, before, stream.FormingLen())
	require.Equal(t, 0, stream.EventCount(), "oversize forming must not publish a durable event")
}

// slowWriter sleeps before accepting each Write, then succeeds.
type slowWriter struct {
	delay time.Duration
	mu    sync.Mutex
	buf   bytes.Buffer
}

func (w *slowWriter) Header() http.Header { return make(http.Header) }
func (w *slowWriter) WriteHeader(int)     {}
func (w *slowWriter) Flush()              {}
func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *slowWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// blockingHeaderWriter parks inside WriteHeader until release is closed, and
// records any body Write that arrives while the header write is still running.
// Both are the same underlying response, so an interleaved body write would
// corrupt it.
type blockingHeaderWriter struct {
	entered chan struct{}
	release chan struct{}
	hdr     http.Header

	mu           sync.Mutex
	inHeader     bool
	wroteInHdr   bool
	body         bytes.Buffer
	headerCalled int
}

func newBlockingHeaderWriter() *blockingHeaderWriter {
	return &blockingHeaderWriter{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		hdr:     make(http.Header),
	}
}

func (w *blockingHeaderWriter) Header() http.Header { return w.hdr }

func (w *blockingHeaderWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inHeader {
		w.wroteInHdr = true
	}
	return w.body.Write(p)
}

func (w *blockingHeaderWriter) WriteHeader(int) {
	w.mu.Lock()
	w.inHeader = true
	w.headerCalled++
	w.mu.Unlock()
	select {
	case <-w.entered:
	default:
		close(w.entered)
	}
	<-w.release
	w.mu.Lock()
	w.inHeader = false
	w.mu.Unlock()
}

func (w *blockingHeaderWriter) interleaved() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wroteInHdr
}

func (w *blockingHeaderWriter) bodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestLiveStream_WriteHeaderDoesNotHoldLockDuringPrimaryIO(t *testing.T) {
	stream := newSpooledLiveStream(t)
	stream.Header().Set("Content-Type", "text/event-stream")
	w := newBlockingHeaderWriter()
	stream.SetPrimary(w)

	done := make(chan struct{})
	go func() {
		defer close(done)
		stream.WriteHeader(http.StatusOK)
	}()

	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("WriteHeader never reached primary")
	}

	// The producer must not be blocked by slow primary header I/O...
	const line = "data: while-header-blocked\n"
	produced := make(chan struct{})
	go func() {
		_, _ = stream.Write([]byte(line))
		close(produced)
	}()
	select {
	case <-produced:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ML producer blocked while primary WriteHeader was slow")
	}
	require.Equal(t, int64(len(line)), stream.TotalBytes())

	// ...but subscriber #0 must not write the body into the same response
	// while WriteHeader is still running.
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, w.bodyString(), "body must not be written during header I/O")

	close(w.release)
	stream.Close(nil)
	stream.WaitPrimary()

	require.False(t, w.interleaved(), "body write interleaved with WriteHeader")
	require.Equal(t, line, stripDevshardTSComments(w.bodyString()), "body must land after the header write")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WriteHeader never returned")
	}
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
}

func TestLiveStream_PrimaryDrainNotBlockedWhenHeaderNeverWritten(t *testing.T) {
	// hdrInFlight, not wroteHdr: a producer that never writes a header must
	// still get its body drained.
	stream := newSpooledLiveStream(t)
	rec := newFlushRecorder()
	stream.SetPrimary(rec)

	_, err := stream.Write([]byte("data: no-header\n"))
	require.NoError(t, err)
	stream.Close(nil)
	stream.WaitPrimary()

	require.Equal(t, "data: no-header\n", stripDevshardTSComments(rec.body()))
}

func TestLiveStream_ClosedSubscribersAreRemoved(t *testing.T) {
	stream := newSpooledLiveStream(t)
	_, err := stream.Write([]byte("data: seed\n"))
	require.NoError(t, err)
	stream.Close(nil)

	const n = 20
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		require.NoError(t, stream.Subscribe(rec, 0, 0))
	}
	require.Equal(t, 0, stream.subscriberCount(),
		"closed reconnect subscribers must not accumulate in the slice")
}

func TestLiveStream_PrimaryUnsubscribeCompactsSubs(t *testing.T) {
	stream := newSpooledLiveStream(t)
	w := httptest.NewRecorder()
	stream.SetPrimary(w)
	require.Equal(t, 1, stream.subscriberCount())

	_, err := stream.Write([]byte("data: body\n"))
	require.NoError(t, err)
	stream.Close(nil)
	stream.WaitPrimary()

	deadline := time.After(2 * time.Second)
	for stream.subscriberCount() != 0 {
		select {
		case <-deadline:
			t.Fatalf("primary still registered after WaitPrimary: %d", stream.subscriberCount())
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

var _ http.ResponseWriter = (*LiveStream)(nil)

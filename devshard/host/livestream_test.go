package host

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

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
	stream := newLiveStream()
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
	body := rec.Body.Bytes()
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
		got = append(got, stream.copyPendingLocked(sub)...)
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

func TestLiveStream_CursorPastBuffer(t *testing.T) {
	stream := newLiveStream()
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	err = stream.Subscribe(rec, 5, 0)
	require.ErrorIs(t, err, ErrResumeCursorPast)
}

// Real SSE framing is "data: …\n\n". Gateway delivered_events counts only
// non-blank lines; host must use the same space or reconnect duplicates content.
func TestLiveStream_CursorMatchesGatewayDoubleNewlineFraming(t *testing.T) {
	stream := newLiveStream()
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

	got := rec.Body.String()
	require.Empty(t, got, "cursor(3,0) must resume past all delivered content; got %q", got)
}

func TestLiveStream_ReconnectAfterTwoOfThreeDoubleNewlineEvents(t *testing.T) {
	stream := newLiveStream()
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

	got := rec.Body.String()
	require.Contains(t, got, "data: C")
	require.NotContains(t, got, "data: A")
	require.NotContains(t, got, "data: B")
	require.Equal(t, 1, bytes.Count([]byte(got), []byte("data: C")))
}

func TestLiveStream_NegativeCursorRejected(t *testing.T) {
	stream := newLiveStream()
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

// gateWriter blocks inside Write until release is closed. Used to prove
// readers do not hold LiveStream.mu across client I/O.
type gateWriter struct {
	onFirstWrite chan struct{}
	release      chan struct{}
	mu           sync.Mutex
	buf          bytes.Buffer
}

func (g *gateWriter) Header() http.Header { return make(http.Header) }
func (g *gateWriter) WriteHeader(int)     {}
func (g *gateWriter) Flush()              {}
func (g *gateWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	_, _ = g.buf.Write(p)
	g.mu.Unlock()
	select {
	case <-g.onFirstWrite:
	default:
		close(g.onFirstWrite)
	}
	<-g.release
	return len(p), nil
}

func (g *gateWriter) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.String()
}

func TestLiveStream_ReplayDoesNotHoldLockDuringClientWrite(t *testing.T) {
	stream := newLiveStream()
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	_, err = stream.Write([]byte("data: two\n"))
	require.NoError(t, err)

	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
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
	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
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

	// Client Write is blocked; keep producing — bytes must be retained in the log.
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
	stream := newLiveStream()
	stream.createdAt = time.Now().Add(-InflightReplayBufferTTL - time.Second)
	rec := httptest.NewRecorder()
	err := stream.Subscribe(rec, 0, 0)
	require.ErrorIs(t, err, ErrLiveStreamPruned)
}

func TestLiveStream_OverCapRefusesNewSubscribe(t *testing.T) {
	prev := LiveStreamMaxRAMBytes
	LiveStreamMaxRAMBytes = 64
	t.Cleanup(func() { LiveStreamMaxRAMBytes = prev })

	stream := newLiveStream()
	// Pin a reader at offset 0 so head-trim cannot clear the soft-cap latch.
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()
	_, err := stream.Write([]byte("data: pin\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("pinned reader never started writing")
	}

	_, err = stream.Write([]byte("data: " + strings.Repeat("x", 80) + "\n"))
	require.NoError(t, err)
	require.True(t, stream.RetainedBytes() > LiveStreamMaxRAMBytes)
	require.True(t, stream.OverCap())

	err = stream.Subscribe(httptest.NewRecorder(), 0, 0)
	require.ErrorIs(t, err, ErrLiveStreamOverCap)

	close(w.release)
	stream.Close(nil)
	<-subDone
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
	stream := newLiveStream()
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
	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
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

func TestLiveStream_PrimaryDoesNotBlockProducer(t *testing.T) {
	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
	stream.SetPrimary(w)

	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("primary never started writing")
	}

	produced := make(chan struct{})
	go func() {
		_, _ = stream.Write([]byte("data: two\n"))
		close(produced)
	}()
	select {
	case <-produced:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ML producer blocked on slow primary Write")
	}

	close(w.release)
	stream.Close(nil)
	waitBodyContains(t, w.String, "data: two", time.Second)
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

	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}), // never released
	}
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

	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}), // never released
	}
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
	prevCap := LiveStreamMaxRAMBytes
	LiveStreamReaderStallTimeout = 200 * time.Millisecond
	LiveStreamMaxRAMBytes = 64
	t.Cleanup(func() {
		LiveStreamReaderStallTimeout = prevStall
		LiveStreamMaxRAMBytes = prevCap
	})

	stream := newLiveStream()
	w := &slowWriter{delay: 15 * time.Millisecond}
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()

	for i := 0; i < 8; i++ {
		_, err := stream.Write([]byte(fmt.Sprintf("data: chunk-%d-%s\n", i, strings.Repeat("x", 20))))
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
	}
	stream.Close(nil)

	select {
	case err := <-subDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("slow-but-alive subscribe did not finish")
	}
	require.Contains(t, w.String(), "chunk-0")
	require.Contains(t, w.String(), "chunk-7")
}

func TestLiveStream_CaughtUpIdleWaitNotStalled(t *testing.T) {
	prevStall := LiveStreamReaderStallTimeout
	LiveStreamReaderStallTimeout = 30 * time.Millisecond
	t.Cleanup(func() { LiveStreamReaderStallTimeout = prevStall })

	stream := newLiveStream()
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

func TestLiveStream_HeadTrimBoundsRetainedAndCursor(t *testing.T) {
	prevCap := LiveStreamMaxRAMBytes
	LiveStreamMaxRAMBytes = 64
	t.Cleanup(func() { LiveStreamMaxRAMBytes = prevCap })

	stream := newLiveStream()
	control := newLiveStream()
	var wrote [][]byte
	for i := 0; i < 10; i++ {
		ev := []byte(fmt.Sprintf("data: e%d-%s\n", i, strings.Repeat("y", 12)))
		wrote = append(wrote, ev)
		_, err := stream.Write(ev)
		require.NoError(t, err)
		_, err = control.Write(ev)
		require.NoError(t, err)
	}
	require.LessOrEqual(t, stream.RetainedBytes(), LiveStreamMaxRAMBytes)
	require.Greater(t, stream.BytesBase(), int64(0))

	// Cursor inside the retained window: byte-identical to an untrimmed control.
	inWindowEvents := int64(0)
	stream.mu.Lock()
	base := stream.eventsBase
	stream.mu.Unlock()
	inWindowEvents = base // first retained content event index

	rec := httptest.NewRecorder()
	ctrlRec := httptest.NewRecorder()
	stream.Close(nil)
	control.Close(nil)
	require.NoError(t, stream.Subscribe(rec, inWindowEvents, 0))
	require.NoError(t, control.Subscribe(ctrlRec, inWindowEvents, 0))
	require.Equal(t, ctrlRec.Body.String(), rec.Body.String())

	// Cursor older than the trimmed prefix.
	require.ErrorIs(t, stream.Subscribe(httptest.NewRecorder(), 0, 0), ErrResumeCursorPast)
}

func TestLiveStream_TrimNeverCrossesLiveReader(t *testing.T) {
	prevCap := LiveStreamMaxRAMBytes
	LiveStreamMaxRAMBytes = 32
	t.Cleanup(func() { LiveStreamMaxRAMBytes = prevCap })

	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()
	first := []byte("data: head\n")
	_, err := stream.Write(first)
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("reader never started")
	}
	for i := 0; i < 20; i++ {
		_, err := stream.Write([]byte(fmt.Sprintf("data: t%d\n", i)))
		require.NoError(t, err)
	}
	require.Equal(t, int64(0), stream.BytesBase(), "must not trim past a live reader at offset 0")
	require.True(t, stream.RetainedBytes() > LiveStreamMaxRAMBytes)

	close(w.release)
	stream.Close(nil)
	require.NoError(t, <-subDone)
}

func TestLiveStream_OverCapClearsAfterTrim(t *testing.T) {
	prevCap := LiveStreamMaxRAMBytes
	LiveStreamMaxRAMBytes = 64
	t.Cleanup(func() { LiveStreamMaxRAMBytes = prevCap })

	stream := newLiveStream()
	w := &gateWriter{
		onFirstWrite: make(chan struct{}),
		release:      make(chan struct{}),
	}
	subDone := make(chan error, 1)
	go func() {
		subDone <- stream.Subscribe(w, 0, 0)
	}()
	_, err := stream.Write([]byte("data: " + strings.Repeat("a", 40) + "\n"))
	require.NoError(t, err)
	select {
	case <-w.onFirstWrite:
	case <-time.After(time.Second):
		t.Fatal("reader never started")
	}
	_, err = stream.Write([]byte("data: " + strings.Repeat("b", 40) + "\n"))
	require.NoError(t, err)
	require.True(t, stream.OverCap())

	close(w.release)
	// Reader advances and unsubscribes on Close; trim should clear overCap.
	stream.Close(nil)
	require.NoError(t, <-subDone)

	deadline := time.After(time.Second)
	for stream.OverCap() {
		select {
		case <-deadline:
			t.Fatal("overCap did not clear after reader drained and trimmed")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	require.LessOrEqual(t, stream.RetainedBytes(), LiveStreamMaxRAMBytes)
	require.NoError(t, stream.Subscribe(httptest.NewRecorder(), stream.eventsBaseOrZero(), 0))
}

func (s *LiveStream) eventsBaseOrZero() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventsBase
}

func TestLiveStream_FormingOversizeClosesStream(t *testing.T) {
	prev := LiveStreamMaxFormingBytes
	LiveStreamMaxFormingBytes = 32
	t.Cleanup(func() { LiveStreamMaxFormingBytes = prev })

	stream := newLiveStream()
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

var _ http.ResponseWriter = (*LiveStream)(nil)

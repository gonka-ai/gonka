package host

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFormatParseDevshardTSCommentRoundTrip(t *testing.T) {
	line := FormatDevshardTSComment(128, []int64{100, 200}, []int64{110, 210}, HopTierLive)
	require.True(t, strings.HasPrefix(line, DevshardTSCommentPrefix))
	batch, ok := ParseDevshardTSComment(line)
	require.True(t, ok)
	require.Equal(t, int64(128), batch.B)
	require.Equal(t, []int64{100, 200}, batch.ML)
	require.Equal(t, []int64{110, 210}, batch.W)
	require.Equal(t, HopTierLive, batch.T)
}

func TestParseDevshardTSCommentRejectsMalformed(t *testing.T) {
	_, ok := ParseDevshardTSComment(": something else")
	require.False(t, ok)
	_, ok = ParseDevshardTSComment(DevshardTSCommentPrefix + `{`)
	require.False(t, ok)
	_, ok = ParseDevshardTSComment(DevshardTSCommentPrefix + `{"b":0,"ml":[1],"w":[1,2]}`)
	require.False(t, ok)
}

func TestLiveStream_MLTimesAlignWithContentEvents(t *testing.T) {
	s := newLiveStream()
	_, err := s.Write([]byte("data: a\n\ndata: b\n"))
	require.NoError(t, err)
	ml := s.MLTimes()
	require.Len(t, ml, 2, "blank separator must not add an ml entry")
	require.NotZero(t, ml[0])
	require.NotZero(t, ml[1])
}

func TestLiveStream_HopCommentsOutsideCursorLog(t *testing.T) {
	s := newSpooledLiveStream(t)
	ev := "data: one\n\ndata: two\n"
	_, err := s.Write([]byte(ev))
	require.NoError(t, err)
	s.Close(nil)

	// Log retained bytes are still just the ML data lines.
	require.Equal(t, int64(len(ev)), s.TotalBytes())

	rec := newFlushRecorder()
	require.NoError(t, s.Subscribe(rec, 0, 0))
	got := rec.body()
	require.Contains(t, got, DevshardTSCommentPrefix)
	require.Equal(t, ev, stripDevshardTSComments(got))

	// Resume cursor unchanged: same content after strip.
	rec2 := newFlushRecorder()
	require.NoError(t, s.Subscribe(rec2, 1, 0))
	require.Equal(t, "data: two\n", stripDevshardTSComments(rec2.body()))
}

func TestLiveStream_ReconnectEmitsOriginalMLFreshW(t *testing.T) {
	s := newSpooledLiveStream(t)
	_, err := s.Write([]byte("data: a\n"))
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	_, err = s.Write([]byte("data: b\n"))
	require.NoError(t, err)
	ml := s.MLTimes()
	require.Len(t, ml, 2)
	s.Close(nil)

	rec := newFlushRecorder()
	before := time.Now().UnixMilli()
	require.NoError(t, s.Subscribe(rec, 0, 0))
	after := time.Now().UnixMilli()

	var seenML []int64
	var seenW []int64
	for _, line := range strings.Split(rec.body(), "\n") {
		batch, ok := ParseDevshardTSComment(line)
		if !ok {
			continue
		}
		seenML = append(seenML, batch.ML...)
		seenW = append(seenW, batch.W...)
	}
	require.Equal(t, ml, seenML, "replay must use original ml")
	require.Len(t, seenW, 2)
	for _, w := range seenW {
		require.GreaterOrEqual(t, w, before)
		require.LessOrEqual(t, w, after)
	}
}

func TestDevshardTSBatchJSONStable(t *testing.T) {
	raw, err := json.Marshal(DevshardTSBatch{B: 1, ML: []int64{2}, W: []int64{3}, T: HopTierCache})
	require.NoError(t, err)
	require.JSONEq(t, `{"b":1,"ml":[2],"w":[3],"t":"cache"}`, string(raw))
}

func TestAppendDevshardTSCommentRejectsOversizedBatch(t *testing.T) {
	ml := make([]int64, MaxDevshardTSEventsPerComment+1)
	w := make([]int64, len(ml))
	require.Empty(t, FormatDevshardTSComment(0, ml, w, HopTierLive))
	require.Nil(t, AppendDevshardTSComment(nil, 0, ml, w, HopTierLive))
}

func TestLiveStream_OneHopCommentPerWrite(t *testing.T) {
	// Catch-up with more content events than MaxDevshardTSEventsPerComment must
	// emit repeated (comment + ≤N data) units — never stacked comments.
	s := newSpooledLiveStream(t)
	const n = MaxDevshardTSEventsPerComment + 17
	var want strings.Builder
	for i := 0; i < n; i++ {
		ev := "data: x\n"
		want.WriteString(ev)
		_, err := s.Write([]byte(ev))
		require.NoError(t, err)
	}
	s.Close(nil)

	rec := newFlushRecorder()
	require.NoError(t, s.Subscribe(rec, 0, 0))
	body := rec.body()
	require.Equal(t, want.String(), stripDevshardTSComments(body))

	var (
		comments int
		prevComment bool
		seenML int
	)
	for _, line := range strings.Split(body, "\n") {
		if batch, ok := ParseDevshardTSComment(line); ok {
			require.False(t, prevComment, "stacked comments break gateway pending pairing")
			require.LessOrEqual(t, len(batch.ML), MaxDevshardTSEventsPerComment)
			require.NotEmpty(t, batch.ML)
			seenML += len(batch.ML)
			comments++
			prevComment = true
			continue
		}
		if strings.HasPrefix(line, "data:") {
			prevComment = false
		}
	}
	require.GreaterOrEqual(t, comments, 2, "catch-up must split across writes")
	require.Equal(t, n, seenML)
}

func TestLiveStream_MidEventAttachStampsOpenEvent(t *testing.T) {
	s := newSpooledLiveStream(t)
	ev1 := "data: one\n"
	ev2 := "data: hello-world\n"
	_, err := s.Write([]byte(ev1))
	require.NoError(t, err)
	_, err = s.Write([]byte(ev2))
	require.NoError(t, err)
	ml := s.MLTimes()
	require.Len(t, ml, 2)
	s.Close(nil)

	const partial = int64(10) // inside ev2
	rec := newFlushRecorder()
	before := time.Now().UnixMilli()
	require.NoError(t, s.Subscribe(rec, 1, partial))
	after := time.Now().UnixMilli()

	body := rec.body()
	require.Equal(t, ev2[partial:], stripDevshardTSComments(body))

	var batches []DevshardTSBatch
	for _, line := range strings.Split(body, "\n") {
		if batch, ok := ParseDevshardTSComment(line); ok {
			batches = append(batches, batch)
		}
	}
	require.Len(t, batches, 1, "mid-event attach must stamp the open event once")
	require.Equal(t, int64(1), batches[0].B)
	require.Equal(t, []int64{ml[1]}, batches[0].ML)
	require.Equal(t, HopTierLive, batches[0].T)
	require.Len(t, batches[0].W, 1)
	require.GreaterOrEqual(t, batches[0].W[0], before)
	require.LessOrEqual(t, batches[0].W[0], after)
	require.True(t, strings.HasPrefix(body, DevshardTSCommentPrefix),
		"stamp must precede remainder on a fresh body")
}

func TestLiveStream_MidEventFormingOmitsOpenStamp(t *testing.T) {
	// Event still in forming has no ml[] entry yet — cannot stamp.
	s := newSpooledLiveStream(t)
	_, err := s.Write([]byte("data: one\n"))
	require.NoError(t, err)
	partial := []byte("data: hello-world")
	_, err = s.Write(partial[:10])
	require.NoError(t, err)
	require.Equal(t, 1, len(s.MLTimes()))

	rec := newFlushRecorder()
	done := make(chan error, 1)
	go func() { done <- s.Subscribe(rec, 1, 4) }()
	waitBodyContains(t, rec.body, string(partial[4:10]), 500*time.Millisecond)
	require.NotContains(t, rec.body(), DevshardTSCommentPrefix)

	_, err = s.Write(append(partial[10:], '\n'))
	require.NoError(t, err)
	s.Close(nil)
	require.NoError(t, <-done)
}

func TestLiveStream_SpoolCatchupOmitsComments(t *testing.T) {
	prevRing := LiveStreamRingBytes
	LiveStreamRingBytes = 64
	t.Cleanup(func() { LiveStreamRingBytes = prevRing })

	s := newSpooledLiveStream(t)
	var want strings.Builder
	for i := 0; i < 20; i++ {
		ev := "data: " + strings.Repeat("x", 40) + "\n"
		want.WriteString(ev)
		_, err := s.Write([]byte(ev))
		require.NoError(t, err)
	}
	waitRetainedAtMost(t, s, LiveStreamRingBytes+80)
	s.Close(nil)

	rec := httptest.NewRecorder()
	require.NoError(t, s.Subscribe(rec, 0, 0))
	body := rec.Body.String()
	// Early events were spooled+trimmed: those ranges emit without comments.
	// Trailing RAM window may still carry comments. Stripped body must match.
	require.Equal(t, want.String(), stripDevshardTSComments(body))
}

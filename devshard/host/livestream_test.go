package host

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type flushRecorder struct {
	*httptest.ResponseRecorder
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushRecorder) Flush() {}

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

	// Remainder of the buffered partial event must arrive before Close.
	deadline := time.After(500 * time.Millisecond)
	for {
		if bytes.Contains(rec.Body.Bytes(), restOfPartial) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected partial remainder %q before completion, got %q", restOfPartial, rec.Body.Bytes())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

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

func TestLiveStream_CursorPastBuffer(t *testing.T) {
	stream := newLiveStream()
	_, err := stream.Write([]byte("data: one\n"))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	err = stream.Subscribe(rec, 5, 0)
	require.ErrorIs(t, err, ErrResumeCursorPast)
}

func TestLiveStream_PruneTTL(t *testing.T) {
	stream := newLiveStream()
	stream.createdAt = time.Now().Add(-InflightReplayBufferTTL - time.Second)
	rec := httptest.NewRecorder()
	err := stream.Subscribe(rec, 0, 0)
	require.ErrorIs(t, err, ErrLiveStreamPruned)
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

	require.Contains(t, primary.Body.String(), "data: hi")
	require.Contains(t, subRec.Body.String(), "data: hi")
}

var _ http.ResponseWriter = (*LiveStream)(nil)

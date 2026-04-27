package main

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
)

func TestSseChunkHasContent(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "empty", body: "", want: false},
		{name: "done_only", body: "data: [DONE]\n\n", want: false},
		{
			name: "role_chunk_only",
			body: `data: {"id":"a","choices":[{"delta":{"role":"assistant"},"index":0,"finish_reason":null}]}` + "\n\n",
			want: false,
		},
		{
			name: "finish_only",
			body: `data: {"id":"a","choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}` + "\n\n",
			want: false,
		},
		{
			name: "delta_content",
			body: `data: {"id":"a","choices":[{"delta":{"content":"Hello"},"index":0}]}` + "\n\n",
			want: true,
		},
		{
			name: "delta_reasoning",
			body: `data: {"id":"a","choices":[{"delta":{"reasoning_content":"hmm"},"index":0}]}` + "\n\n",
			want: true,
		},
		{
			name: "delta_tool_calls",
			body: `data: {"id":"a","choices":[{"delta":{"tool_calls":[{"id":"x"}]},"index":0}]}` + "\n\n",
			want: true,
		},
		{
			name: "delta_tool_calls_empty_array",
			body: `data: {"id":"a","choices":[{"delta":{"tool_calls":[]},"index":0}]}` + "\n\n",
			want: false,
		},
		{
			// The `42g7kr9d` pattern: a host wraps the non-streaming response
			// shape inside a single SSE event. Redundancy should still count
			// that as usable convertible content so the attempt can win; the
			// streaming writer is responsible for rewriting it into chunk
			// form before it reaches the client.
			name: "message_content_convertible",
			body: `data: {"choices":[{"message":{"content":"stub"}}],"usage":{}}` + "\n\n",
			want: true,
		},
		{
			// Legacy /v1/completions shape. Our streaming path only serves
			// /v1/chat/completions; a host emitting `text` here produces
			// the same unrenderable failure mode.
			name: "completion_text_field_rejected",
			body: `data: {"choices":[{"text":"abc"}]}` + "\n\n",
			want: false,
		},
		{
			name: "message_reasoning_rejected",
			body: `data: {"choices":[{"message":{"reasoning_content":"stub"}}]}` + "\n\n",
			want: false,
		},
		{
			name: "message_tool_calls_rejected",
			body: `data: {"choices":[{"message":{"tool_calls":[{"id":"x"}]}}]}` + "\n\n",
			want: false,
		},
		{
			name: "role_then_content_in_one_write",
			body: `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
				`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			want: true,
		},
		{
			name: "malformed_json",
			body: "data: {not_json}\n\n",
			want: false,
		},
		{
			name: "non_data_lines_ignored",
			body: "event: ping\nid: 42\n\n",
			want: false,
		},
		{
			name: "delta_empty_string",
			body: `data: {"choices":[{"delta":{"content":""}}]}` + "\n\n",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sseChunkHasContent([]byte(tc.body))
			require.Equal(t, tc.want, got)
		})
	}
}

// TestSseChunkContentSource locks in the forensic classification used for
// short-content winner diagnostics. The label in the log identifies which
// field carried the first content event; `""` means none of the accepted
// streaming fields were populated.
func TestSseChunkContentSource(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSource string
		wantOK     bool
	}{
		{
			name:       "delta_content",
			body:       `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			wantSource: "delta.content",
			wantOK:     true,
		},
		{
			name:       "delta_reasoning",
			body:       `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}` + "\n\n",
			wantSource: "delta.reasoning_content",
			wantOK:     true,
		},
		{
			name:       "delta_tool_calls",
			body:       `data: {"choices":[{"delta":{"tool_calls":[{"id":"x"}]}}]}` + "\n\n",
			wantSource: "delta.tool_calls",
			wantOK:     true,
		},
		{
			// Content precedence: delta.content wins over the others when
			// more than one field is populated in the same event.
			name:       "delta_content_precedence",
			body:       `data: {"choices":[{"delta":{"content":"hi","reasoning_content":"think"}}]}` + "\n\n",
			wantSource: "delta.content",
			wantOK:     true,
		},
		{
			name:       "message_content_convertible",
			body:       `data: {"choices":[{"message":{"content":"stub"}}]}` + "\n\n",
			wantSource: "message.content",
			wantOK:     true,
		},
		{
			name:       "text_rejected",
			body:       `data: {"choices":[{"text":"abc"}]}` + "\n\n",
			wantSource: "",
			wantOK:     false,
		},
		{
			name:       "role_only",
			body:       `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
			wantSource: "",
			wantOK:     false,
		},
		{
			name:       "done",
			body:       "data: [DONE]\n\n",
			wantSource: "",
			wantOK:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, ok := sseChunkContentSource([]byte(tc.body))
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.wantSource, src)
		})
	}
}

// TestRaceWriter_RecordsContentSourceAndSample verifies that the forensic
// fields on inflight (contentSource, firstBytesSample) are populated by the
// race writer so the enriched race_completed/empty_stream logs carry the
// evidence needed to diagnose short-content winners like `42g7kr9d`.
func TestRaceWriter_RecordsContentSourceAndSample(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	inf := &inflight{
		hostID:       "host-A",
		escrowID:     "escrow-x",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}

	role := []byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n")
	_, err := rw.Write(role)
	require.NoError(t, err)
	require.Equal(t, "", inf.contentSource, "role-only chunk must not set contentSource")

	content := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	_, err = rw.Write(content)
	require.NoError(t, err)
	require.Equal(t, "delta.content", inf.contentSource)

	// A later chunk with a different source must NOT overwrite the first.
	more := []byte(`data: {"choices":[{"delta":{"tool_calls":[{"id":"x"}]}}]}` + "\n\n")
	_, err = rw.Write(more)
	require.NoError(t, err)
	require.Equal(t, "delta.content", inf.contentSource, "first content source wins")
}

func TestRaceWriter_MessageContentCountsAsConvertibleContent(t *testing.T) {
	ctx := context.Background()
	body := []byte(`data: {"choices":[{"message":{"role":"assistant","content":"hi"}}]}` + "\n\n")
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)
	inf := &inflight{
		hostID:       "host-A",
		escrowID:     "escrow-x",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}
	_, err := rw.Write(body)
	require.NoError(t, err)
	require.Equal(t, int64(1), inf.contentChunks.Load())
	require.Equal(t, "message.content", inf.contentSource)
}

func TestIsEmptyStreamAttempt(t *testing.T) {
	t.Run("nil_inflight", func(t *testing.T) {
		require.False(t, isEmptyStreamAttempt(nil))
	})
	t.Run("probe_never_empty", func(t *testing.T) {
		inf := &inflight{probe: true, receiptTime: time.Now()}
		inf.outputChunks.Store(2)
		require.False(t, isEmptyStreamAttempt(inf))
	})
	t.Run("no_receipt_no_bytes", func(t *testing.T) {
		// In-process test path: receipt callback never fires.
		// Must not be flagged so test fixtures aren't broken.
		inf := &inflight{}
		require.False(t, isEmptyStreamAttempt(inf))
	})
	t.Run("no_receipt_with_bytes_no_content", func(t *testing.T) {
		// Defensive: if somehow bytes appear without a receipt, we still
		// don't flag — the receipt gate is the source of truth.
		inf := &inflight{}
		inf.outputChunks.Store(2)
		require.False(t, isEmptyStreamAttempt(inf))
	})
	t.Run("receipt_bytes_no_content", func(t *testing.T) {
		// Original empty-SSE pattern: role marker + [DONE] only.
		inf := &inflight{receiptTime: time.Now()}
		inf.outputChunks.Store(2)
		require.True(t, isEmptyStreamAttempt(inf))
	})
	t.Run("receipt_no_bytes_at_all_stall", func(t *testing.T) {
		// Stall pattern (369pqtgx-class): host got the receipt, then
		// went silent for the full deadline. No bytes streamed at all.
		inf := &inflight{receiptTime: time.Now()}
		require.True(t, isEmptyStreamAttempt(inf))
	})
	t.Run("receipt_bytes_with_content", func(t *testing.T) {
		inf := &inflight{receiptTime: time.Now()}
		inf.outputChunks.Store(3)
		inf.contentChunks.Store(1)
		require.False(t, isEmptyStreamAttempt(inf))
	})
}

// TestRaceWriter_BuffersRoleUntilContent verifies that a single attempt
// streaming role-chunk -> content-chunk -> [DONE] produces correctly ordered
// output to the race group writer with no winner declared until content
// arrives.
func TestRaceWriter_BuffersRoleUntilContent(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	inf := &inflight{
		hostID:       "host-A",
		escrowID:     "escrow-x",
		nonce:        1,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}

	roleChunk := []byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n")
	n, err := rw.Write(roleChunk)
	require.NoError(t, err)
	require.Equal(t, len(roleChunk), n)
	require.Equal(t, uint64(0), rg.winnerNonce(), "no winner before content")
	require.Equal(t, 0, sink.Len(), "role chunk should be buffered, not forwarded")
	require.Equal(t, int64(1), inf.outputChunks.Load())
	require.Equal(t, int64(0), inf.contentChunks.Load())

	contentChunk := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	n, err = rw.Write(contentChunk)
	require.NoError(t, err)
	require.Equal(t, len(contentChunk), n)
	require.Equal(t, uint64(1), rg.winnerNonce(), "winner set on first content chunk")
	require.Equal(t, int64(1), inf.contentChunks.Load())
	got := sink.String()
	require.Contains(t, got, `"role":"assistant"`, "buffered role chunk must be flushed in order")
	require.Contains(t, got, `"content":"hi"`)
	require.Less(t, bytes.Index(sink.Bytes(), []byte("role")), bytes.Index(sink.Bytes(), []byte("content")), "role chunk must precede content chunk")
	require.Nil(t, inf.pendingBuf, "buffer cleared after flush")

	doneChunk := []byte("data: [DONE]\n\n")
	n, err = rw.Write(doneChunk)
	require.NoError(t, err)
	require.Equal(t, len(doneChunk), n)
	require.Contains(t, sink.String(), "[DONE]")
}

// TestRaceWriter_EmptyAttemptDoesNotWin covers the core empty-stream guard:
// an attempt that only streams role + DONE (no content) must not become the
// winner, must not flush bytes to the client, and must drop its buffer.
func TestRaceWriter_EmptyAttemptDoesNotWin(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	inf := &inflight{
		hostID:       "empty-host",
		escrowID:     "escrow-x",
		nonce:        1,
		receiptTime:  time.Now(),
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: rg, nonce: 1, inf: inf}

	role := []byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n")
	done := []byte("data: [DONE]\n\n")
	_, err := rw.Write(role)
	require.NoError(t, err)
	_, err = rw.Write(done)
	require.NoError(t, err)

	require.Equal(t, uint64(0), rg.winnerNonce(), "empty stream must not declare a winner")
	require.Equal(t, 0, sink.Len(), "no bytes forwarded for empty stream")
	require.Equal(t, int64(2), inf.outputChunks.Load())
	require.Equal(t, int64(0), inf.contentChunks.Load())
	require.True(t, isEmptyStreamAttempt(inf))
}

// TestRaceWriter_ContentProducerWinsOverEmpty verifies that when one attempt
// streams only role/DONE and a competing attempt streams real content, the
// content-producing attempt wins, its bytes are forwarded, and the empty
// attempt's later writes are suppressed.
func TestRaceWriter_ContentProducerWinsOverEmpty(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow-x", &sink)

	infEmpty := &inflight{
		hostID: "empty-host", escrowID: "escrow-x", nonce: 1,
		done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{}),
	}
	infGood := &inflight{
		hostID: "good-host", escrowID: "escrow-x", nonce: 2,
		done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{}),
	}
	rwEmpty := &raceWriter{group: rg, nonce: 1, inf: infEmpty}
	rwGood := &raceWriter{group: rg, nonce: 2, inf: infGood}

	// Empty host streams role first; should buffer, no winner.
	_, err := rwEmpty.Write([]byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"))
	require.NoError(t, err)
	require.Equal(t, uint64(0), rg.winnerNonce())
	require.Equal(t, 0, sink.Len())

	// Good host now streams role + content in one chunk. It should win.
	goodPayload := []byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"world"}}]}` + "\n\n")
	_, err = rwGood.Write(goodPayload)
	require.NoError(t, err)
	require.Equal(t, uint64(2), rg.winnerNonce(), "content-producing attempt wins")
	require.Contains(t, sink.String(), `"content":"world"`)
	require.Equal(t, int64(1), infGood.contentChunks.Load())

	// Empty host streams DONE after losing; bytes must not be forwarded and
	// its buffer must be discarded.
	preWrite := sink.Len()
	_, err = rwEmpty.Write([]byte("data: [DONE]\n\n"))
	require.NoError(t, err)
	require.Equal(t, preWrite, sink.Len(), "loser writes must be suppressed")
	require.Nil(t, infEmpty.pendingBuf, "loser buffer discarded once another attempt wins")
}

// TestInflightFinished_StallHostNotFinished verifies the stall pattern that
// motivated this change: a host returns receipt quickly, the chain protocol
// records MsgFinishInference, but the host never streams any content. Even
// with a finish marker present, inflightFinished must report false so the
// race loop falls through to retry/timeout instead of crowning a silent host.
func TestInflightFinished_StallHostNotFinished(t *testing.T) {
	resp := &host.HostResponse{
		Mempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: 7},
			}},
		},
	}

	stall := &inflight{
		hostID:      "stall-host",
		nonce:       7,
		receiptTime: time.Now(),
		resp:        resp,
	}
	require.True(t, isEmptyStreamAttempt(stall), "stall pattern must be flagged")
	require.False(t, inflightFinished(stall),
		"stalled attempt with finish marker must NOT count as finished")

	good := &inflight{
		hostID:      "good-host",
		nonce:       7,
		receiptTime: time.Now(),
		resp:        resp,
	}
	good.outputChunks.Store(2)
	good.contentChunks.Store(1)
	require.False(t, isEmptyStreamAttempt(good))
	require.True(t, inflightFinished(good), "real producer must count as finished")

	noReceipt := &inflight{
		hostID: "in-process",
		nonce:  7,
		resp:   resp,
	}
	require.False(t, isEmptyStreamAttempt(noReceipt),
		"path that never confirmed receipt must not be flagged as stall")
	require.True(t, inflightFinished(noReceipt),
		"in-process style attempt still counts via protocol finish marker")
}

func TestErrEmptyStreamSentinel(t *testing.T) {
	require.True(t, errors.Is(errEmptyStream, errEmptyStream))
	// Make sure the sentinel is unique and not nil.
	var x error = errEmptyStream
	require.NotNil(t, x)
	require.Contains(t, errEmptyStream.Error(), "empty content stream")
}

// Compile-time confirmation that contentChunks lives on inflight as an
// atomic.Int64; protects against accidental refactors that drop the field.
var _ = func() bool {
	var inf inflight
	_ = atomic.Int64{}
	_ = inf.contentChunks.Load()
	return true
}

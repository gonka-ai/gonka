package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

func TestDeliveredPrefix_FragmentedWrites(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	rw := mkRaceWriter(t, inf)

	event1 := []byte(`data: {"choices":[{"delta":{"content":"Hi"}}]}` + "\n")
	event2 := []byte(`data: {"choices":[{"delta":{"content":"!"}}]}` + "\n")

	// Byte-at-a-time through event1.
	for i := 0; i < len(event1); i++ {
		_, err := rw.Write(event1[i : i+1])
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), inf.deliveredEvents)
	require.Equal(t, int64(0), inf.deliveredPartial)

	// Partial event2 (no trailing newline yet).
	partial := event2[:len(event2)/2]
	_, err := rw.Write(partial)
	require.NoError(t, err)
	require.Equal(t, int64(1), inf.deliveredEvents)
	require.Equal(t, int64(len(partial)), inf.deliveredPartial)

	rest := event2[len(partial):]
	_, err = rw.Write(rest)
	require.NoError(t, err)
	require.Equal(t, int64(2), inf.deliveredEvents)
	require.Equal(t, int64(0), inf.deliveredPartial)
}

func TestDeliveredPrefix_PendingBufCountsOnlyAfterCrown(t *testing.T) {
	inf := mkRaceWriterInflight(t)
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, inf.escrowID, &sink)
	rw := &raceWriter{group: rg, nonce: inf.nonce, inf: inf}

	role := []byte(`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n")
	_, err := rw.Write(role)
	require.NoError(t, err)
	require.Equal(t, int64(0), inf.deliveredEvents, "pre-crown buffer must not count")
	require.Equal(t, int64(0), inf.deliveredPartial)
	require.NotEmpty(t, inf.pendingBuf)

	content := []byte(`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n")
	_, err = rw.Write(content)
	require.NoError(t, err)
	require.Equal(t, inf.nonce, rg.winner)
	// role + content both forwarded on crown.
	require.Equal(t, int64(2), inf.deliveredEvents)
	require.Equal(t, int64(0), inf.deliveredPartial)
	require.Empty(t, inf.pendingBuf)
}

func TestDeliveredPrefix_LosersAndProbesCountNothing(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	rg := newRaceGroup(ctx, ctx, "escrow", &sink)

	winner := mkRaceWriterInflight(t)
	winner.nonce = 1
	loser := mkRaceWriterInflight(t)
	loser.nonce = 2
	probe := mkRaceWriterInflight(t)
	probe.nonce = 3
	probe.probe = true

	rwWinner := &raceWriter{group: rg, nonce: 1, inf: winner}
	rwLoser := &raceWriter{group: rg, nonce: 2, inf: loser}
	rwProbe := &raceWriter{group: rg, nonce: 3, inf: probe}

	content := []byte(`data: {"choices":[{"delta":{"content":"win"}}]}` + "\n")
	_, err := rwWinner.Write(content)
	require.NoError(t, err)
	require.Equal(t, int64(1), winner.deliveredEvents)

	_, err = rwLoser.Write(content)
	require.NoError(t, err)
	require.Equal(t, int64(0), loser.deliveredEvents)
	require.Equal(t, int64(0), loser.deliveredPartial)

	_, err = rwProbe.Write(content)
	require.NoError(t, err)
	require.Equal(t, int64(0), probe.deliveredEvents)
	require.Equal(t, int64(0), probe.deliveredPartial)
}

// mkDeferredWinnerWriter builds the production writer chain (raceWriter ->
// deferredWriter -> ResponseWriter) so payload rewriting is in the path.
func mkDeferredWinnerWriter(t testing.TB, flag *cancelFlag) (*raceWriter, *inflight, *httptest.ResponseRecorder) {
	t.Helper()
	ctx := context.Background()
	rec := httptest.NewRecorder()
	dw := newDeferredWriter(ctx, rec, "fixture-escrow", flag)
	rg := newRaceGroup(ctx, ctx, "fixture-escrow", dw)
	rg.clientFlag = flag
	inf := mkRaceWriterInflight(t)
	rw := &raceWriter{group: rg, nonce: inf.nonce, inf: inf}
	rg.setWinner(inf.nonce)
	return rw, inf, rec
}

func TestDeliveredPrefix_ExpandingRewriteCountsUpstreamEvents(t *testing.T) {
	// A `message`-shaped completion on the streaming path is expanded by
	// rewriteStreamingPayload into several chunk events, so the client writer
	// reports far more bytes than it was handed. The cursor is upstream-side
	// (R2) and must not be derived from that count.
	rw, inf, rec := mkDeferredWinnerWriter(t, nil)

	upstream := []byte(`data: {"id":"x","model":"m","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}` + "\n\n")

	n, err := rw.Write(upstream)
	require.NoError(t, err)
	require.Equal(t, len(upstream), n, "must report progress in upstream bytes")
	require.Greater(t, rec.Body.Len(), len(upstream), "rewrite is expected to expand here")
	require.Equal(t, int64(1), inf.deliveredEvents, "one upstream event was forwarded")
	require.Equal(t, int64(0), inf.deliveredPartial)
}

func TestDeliveredPrefix_ShrinkingRewriteCountsUpstreamEvents(t *testing.T) {
	rw, inf, rec := mkDeferredWinnerWriter(t, nil)

	upstream := []byte(`data: {"choices":[{"delta":{"content":"Hi"},` +
		`"token_ids":[1,2,3,4,5,6,7,8,9,10,11,12]}]}` + "\n\n")

	n, err := rw.Write(upstream)
	require.NoError(t, err)
	require.Equal(t, len(upstream), n)
	require.Less(t, rec.Body.Len(), len(upstream), "internal-field filtering is expected to shrink here")
	require.Equal(t, int64(1), inf.deliveredEvents, "a shrunk chunk is still one whole upstream event")
	require.Equal(t, int64(0), inf.deliveredPartial, "cursor must not land mid-event")
}

func TestDeliveredPrefix_ClientGoneDoesNotAdvanceCursor(t *testing.T) {
	flag := newCancelFlag()
	rw, inf, rec := mkDeferredWinnerWriter(t, flag)
	flag.Trigger()

	upstream := []byte(`data: {"choices":[{"delta":{"content":"unseen"}}]}` + "\n\n")
	n, err := rw.Write(upstream)
	require.NoError(t, err)
	require.Equal(t, len(upstream), n)
	require.Zero(t, rec.Body.Len(), "nothing reaches a disconnected client")
	require.Equal(t, int64(0), inf.deliveredEvents, "swallowed bytes are not a delivered prefix")
	require.Equal(t, int64(0), inf.deliveredPartial)
}

func TestRecordDeliveredForward_EmptyAndMultiEvent(t *testing.T) {
	inf := &inflight{}
	inf.recordDeliveredForward(nil)
	require.Equal(t, int64(0), inf.deliveredEvents)

	// "\n\n" framing: blank separators must not count as events.
	inf.recordDeliveredForward([]byte("data: a\n\ndata: b\n\ndata: c"))
	require.Equal(t, int64(2), inf.deliveredEvents)
	require.Equal(t, int64(len("data: c")), inf.deliveredPartial)
}

func TestPrefixSkipWriter_EventBoundary(t *testing.T) {
	var sink bytes.Buffer
	w := &prefixSkipWriter{w: &sink, skipEvents: 1, skipPartial: 0}
	_, err := w.Write([]byte("data: one\n\ndata: two\n\ndata: three\n\n"))
	require.NoError(t, err)
	require.Equal(t, "data: two\n\ndata: three\n\n", sink.String())
}

func TestPrefixSkipWriter_MidEvent(t *testing.T) {
	var sink bytes.Buffer
	full := "data: hello-world\n\n"
	partial := int64(len("data: hel"))
	w := &prefixSkipWriter{w: &sink, skipEvents: 0, skipPartial: partial}
	_, err := w.Write([]byte(full))
	require.NoError(t, err)
	require.Equal(t, "lo-world\n\n", sink.String())
}

func TestPrefixSkipWriter_ProbeResumeTrustsHostRemainder(t *testing.T) {
	var sink bytes.Buffer
	// Mid-event remainder does not start with "data:" — do not skip.
	w := &prefixSkipWriter{
		w:           &sink,
		skipEvents:  1,
		skipPartial: 10,
		probeResume: true,
	}
	_, err := w.Write([]byte(`lo"}}]}` + "\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)
	require.Equal(t, `lo"}}]}`+"\n\ndata: [DONE]\n\n", sink.String())
}

func TestPrefixSkipWriter_ProbeResumeSkipsFullRestart(t *testing.T) {
	var sink bytes.Buffer
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"
	partial := int64(10)
	clientHad := event1 + "\n\n" + event2[:partial]

	w := &prefixSkipWriter{
		w:           &sink,
		skipEvents:  1,
		skipPartial: partial,
		probeResume: true,
	}
	_, err := w.Write([]byte(full))
	require.NoError(t, err)
	require.Equal(t, full[len(clientHad):], sink.String())
}

func TestPrefixSkipWriter_ProbeResumeEventBoundaryFingerprint(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"

	// Host replays from event 0 after an event-boundary disconnect.
	var skipSink bytes.Buffer
	w := &prefixSkipWriter{
		w:            &skipSink,
		skipEvents:   1,
		skipPartial:  0,
		probeResume:  true,
		firstEventFP: sseEventFingerprint([]byte(event1)),
	}
	_, err := w.Write([]byte(full))
	require.NoError(t, err)
	require.Equal(t, event2+"\n\ndata: [DONE]\n\n", skipSink.String())

	// Correct host emits only the remainder (starts with event2) — trust it.
	var trustSink bytes.Buffer
	w2 := &prefixSkipWriter{
		w:            &trustSink,
		skipEvents:   1,
		skipPartial:  0,
		probeResume:  true,
		firstEventFP: sseEventFingerprint([]byte(event1)),
	}
	remainder := event2 + "\n\ndata: [DONE]\n\n"
	_, err = w2.Write([]byte(remainder))
	require.NoError(t, err)
	require.Equal(t, remainder, trustSink.String())
}

func TestPrefixSkipWriter_ProbeFormingBounded(t *testing.T) {
	var sink bytes.Buffer
	w := &prefixSkipWriter{
		w:            &sink,
		skipEvents:   1,
		probeResume:  true,
		firstEventFP: sseEventFingerprint([]byte("data: x")),
	}
	// Only blank lines: probe stays undecided; forming must stay capped.
	chunk := bytes.Repeat([]byte("\n"), maxPrefixSkipProbeBytes/2)
	for i := 0; i < 8; i++ {
		_, err := w.Write(chunk)
		require.NoError(t, err)
		require.LessOrEqual(t, len(w.forming), maxPrefixSkipProbeBytes)
	}
	require.False(t, w.probed)
	require.Empty(t, sink.String())
}

func TestPrefixSkipWriter_OversizedLineTrustsHostWithoutLosingBytes(t *testing.T) {
	var sink bytes.Buffer
	w := &prefixSkipWriter{
		w:            &sink,
		skipEvents:   1,
		probeResume:  true,
		firstEventFP: sseEventFingerprint([]byte("data: delivered")),
	}
	// A single line larger than the probe cap has no boundary to compare, so
	// the probe must give up and forward every byte rather than truncate.
	payload := "data: " + strings.Repeat("z", maxPrefixSkipProbeBytes*2)
	_, err := w.Write([]byte(payload))
	require.NoError(t, err)
	_, err = w.Write([]byte("\n\ndata: [DONE]\n\n"))
	require.NoError(t, err)
	require.Equal(t, payload+"\n\ndata: [DONE]\n\n", sink.String())
}

func TestMergeInflightHostResponse_PrefersReceiptAndClearsErr(t *testing.T) {
	inf := &inflight{
		err: errors.New("truncated"),
		resp: &host.HostResponse{
			Receipt:     []byte("old"),
			ConfirmedAt: 1,
		},
	}
	mergeInflightHostResponse(inf, &host.HostResponse{
		Receipt:     []byte("new"),
		ConfirmedAt: 2,
		Mempool:     []*types.DevshardTx{{}},
	}, nil)
	require.NoError(t, inf.err)
	require.Equal(t, []byte("new"), inf.resp.Receipt)
	require.Equal(t, int64(2), inf.resp.ConfirmedAt)
	require.Len(t, inf.resp.Mempool, 1)
}

func TestReconnectInflight_MidEventResumeNoDuplication(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"

	clientHad := event1 + "\n\n" + event2[:10]
	deliveredEvents := int64(1)
	deliveredPartial := int64(10)

	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		expectEvents:    deliveredEvents,
		expectPartial:   deliveredPartial,
		responseReceipt: []byte("receipt-1"),
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	params := defaultParams()
	params.Stream = true

	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(clientHad)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)

	inf := newDoneInflight(prepared, deliveredEvents, deliveredPartial)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt-1"), ConfirmedAt: 9}
	rg.setWinner(inf.nonce)

	err = env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), inf.reconnectTries.Load())
	require.NoError(t, inf.err)
	require.Equal(t, deliveredEvents, client.lastReq.DeliveredEvents)
	require.Equal(t, deliveredPartial, client.lastReq.DeliveredPartial)
	require.Equal(t, prepared.Nonce(), client.lastReq.Nonce)
	require.Equal(t, full, sink.String(), "client-visible stream must be continuous with no gap/dup")
	require.Equal(t, []byte("receipt-1"), inf.resp.Receipt)
}

func TestReconnectInflight_FinishInferenceProcessedAfterResume(t *testing.T) {
	// Transport drop before meta: receipt only, no Finish. Reconnect returns
	// meta with MsgFinishInference; processInflightOnce must still queue it.
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"

	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		expectEvents:    1,
		expectPartial:   0,
		responseReceipt: []byte("receipt-1"),
		includeFinish:   true,
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(event1 + "\n\n")
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.contentChunks.Store(1)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt-1"), ConfirmedAt: 9}
	rg.setWinner(inf.nonce)

	require.False(t, env.session.IsNonceFinished(prepared.Nonce()))
	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.NoError(t, inf.err)
	require.True(t, user.HasMsgFinish(inf.resp.Mempool, prepared.Nonce()),
		"reconnect HostResponse must carry Finish from meta")

	// Same path awaitRace uses after a successful resume.
	failed, ferr := env.proxy.redundancy.winningInflightTerminalFailure(inf)
	require.NoError(t, ferr)
	require.False(t, failed, "winner with Finish after reconnect must not be terminal failure")
	require.True(t, env.session.IsNonceFinished(prepared.Nonce()))
	require.True(t, user.HasMsgFinish(env.session.PendingTxs(), prepared.Nonce()),
		"FinishInference from reconnect meta must be queued for the next diff")
}

func TestRunWinnerReconnectLadder_FinishInferenceProcessedAfterResume(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"

	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		expectEvents:    1,
		expectPartial:   10,
		responseReceipt: []byte("receipt"),
		includeFinish:   true,
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	enableAttemptReconnectForTest(t, 200*time.Millisecond, 2)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(event1 + "\n\n" + event2[:10])
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 10)
	inf.contentChunks.Store(1)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt")}
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true)

	attempts := []*inflight{inf}
	tried := map[string]bool{env.session.HostParticipantKey(inf.hostIdx): true}
	require.NoError(t, env.proxy.redundancy.runWinnerReconnectLadderSync(
		context.Background(), context.Background(), inf, rg, params,
		&attempts, tried, nil, make(chan *inflight, 2),
	))

	require.True(t, user.HasMsgFinish(inf.resp.Mempool, inf.nonce))
	require.NoError(t, env.proxy.redundancy.processInflightOnce(inf))
	require.True(t, env.session.IsNonceFinished(inf.nonce))
	require.True(t, user.HasMsgFinish(env.session.PendingTxs(), inf.nonce),
		"ladder resume must still ProcessResponse Finish into pending txs")
}

func TestReconnectInflight_FinishStillProcessedWhenPriorRespHadNoMempool(t *testing.T) {
	// processOnce must not be consumed on the truncated first response (err != nil
	// short-circuits winningInflightTerminalFailure). Reconnect Finish is the
	// first ProcessResponse and must mark the nonce finished.
	client := &reconnectScriptClient{
		fullBody:        []byte(`data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n" + "data: [DONE]\n\n"),
		expectEvents:    1,
		responseReceipt: []byte("r"),
		includeFinish:   true,
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	inf := newDoneInflight(prepared, 1, 0)
	inf.contentChunks.Store(1)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("r"), ConfirmedAt: 1}
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	rg.setWinner(inf.nonce)

	failed, _ := env.proxy.redundancy.winningInflightTerminalFailure(inf)
	require.True(t, failed, "truncated attempt is still a terminal failure before reconnect")
	require.False(t, env.session.IsNonceFinished(inf.nonce), "must not ProcessResponse before reconnect")

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.NoError(t, env.proxy.redundancy.processInflightOnce(inf))
	require.True(t, env.session.IsNonceFinished(inf.nonce))
	require.True(t, user.HasMsgFinish(env.session.PendingTxs(), inf.nonce))
}

func TestReconnectInflight_ReceiptOnlyIsResumeFailure(t *testing.T) {
	// Live attach failed host-side: the receipt was already written, so the
	// parser sees a terminator and the transport read ends cleanly. R3 requires
	// this to count as a failed try, not a resume.
	client := &reconnectScriptClient{
		fullBody:        nil,
		expectEvents:    1,
		responseReceipt: []byte("receipt-1"),
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n")
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.contentChunks.Store(1)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt-1")}
	rg.setWinner(inf.nonce)

	rerr := env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil)
	require.ErrorIs(t, rerr, errReconnectReceiptOnly)
	require.ErrorIs(t, inf.err, errReconnectReceiptOnly,
		"a resume that delivered nothing must leave the attempt failed")
}

func TestReconnectInflight_TailOnlyResumeWithMempoolSucceeds(t *testing.T) {
	// The client already had every event and only devshard_meta was missing.
	// No stream bytes come back, but the response can still settle the nonce,
	// so this is a successful resume rather than a receipt-only failure.
	body := `data: {"choices":[{"delta":{"content":"Hi"}}]}` + "\n\n"
	client := &reconnectScriptClient{
		fullBody:        []byte(body),
		expectEvents:    1,
		responseReceipt: []byte("receipt-1"),
		includeFinish:   true,
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(body)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.contentChunks.Store(1)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt-1")}
	rg.setWinner(inf.nonce)

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.NoError(t, inf.err)
	require.True(t, user.HasMsgFinish(inf.resp.Mempool, prepared.Nonce()))
}

func TestWinningInflightTerminalFailure_KeepsProcessOnceForReconnect(t *testing.T) {
	// A clean mid-stream close leaves err == nil with a receipt-only response.
	// Spending the one ProcessResponse on it would strand the Finish that the
	// reconnect is still able to merge (R1).
	client := &reconnectScriptClient{ignoreCursor: true}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	enableAttemptReconnectForTest(t, 200*time.Millisecond, 2)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	inf := newDoneInflight(prepared, 1, 0)
	inf.contentChunks.Store(1)
	inf.err = nil
	inf.resp = &host.HostResponse{Receipt: []byte("receipt-1")} // no mempool

	require.True(t, env.proxy.redundancy.canAttemptWinnerReconnect(inf))
	failed, ferr := env.proxy.redundancy.winningInflightTerminalFailure(inf)
	require.True(t, failed, "a winner that cannot settle is a terminal failure for now")
	require.Error(t, ferr)
	require.False(t, env.session.IsNonceFinished(prepared.Nonce()))

	// The ladder later merges a response carrying Finish; it must still be
	// processed, which is only possible if processOnce was left unspent.
	inf.resp = &host.HostResponse{
		Receipt: []byte("receipt-1"),
		Mempool: []*types.DevshardTx{finishInferenceTx(prepared.Nonce())},
	}
	inf.reconnectLadderUsed.Store(true)
	require.NoError(t, env.proxy.redundancy.processInflightOnce(inf))
	require.True(t, env.session.IsNonceFinished(prepared.Nonce()),
		"Finish merged by the reconnect must still reach ProcessResponse")
}

func TestReconnectInflight_EventBoundary(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"one"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"two"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"

	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		expectEvents:    1,
		expectPartial:   0,
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(event1 + "\n\n")
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.err = io.ErrUnexpectedEOF
	inf.resp = &host.HostResponse{Receipt: []byte("r")}
	rg.setWinner(inf.nonce)

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.Equal(t, full, sink.String())
}

func TestReconnectInflight_DoesNotRecordRealSend(t *testing.T) {
	client := &reconnectScriptClient{
		fullBody:        []byte(`data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n" + "data: [DONE]\n\n"),
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	inf := newDoneInflight(prepared, 0, 0)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	rg.setWinner(inf.nonce)

	// reconnectInflight must not go through startInflight / recordGatewayAttemptStarted.
	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.Equal(t, int64(1), inf.reconnectTries.Load())
	require.Equal(t, 1, client.calls)
	require.Equal(t, prepared.Nonce(), client.lastReq.Nonce)
}

func TestReconnectInflight_DefensiveSkipWhenHostReplaysPrefix(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"
	partial := int64(10)
	clientHad := event1 + "\n\n" + event2[:partial]

	// Host ignores cursor and re-sends from the start; gateway mid-event probe
	// must detect the restart and skip.
	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		ignoreCursor:    true,
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(clientHad)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, partial)
	inf.deliveredFirstEventFP = sseEventFingerprint([]byte(event1))
	inf.resp = &host.HostResponse{Receipt: []byte("r")}
	rg.setWinner(inf.nonce)

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.Equal(t, full, sink.String())
}

func TestReconnectInflight_DefensiveSkipEventBoundaryViaFingerprint(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"
	clientHad := event1 + "\n\n"

	// Event-boundary resume: without fingerprint, decideProbe would trust the
	// host and duplicate event1. Fingerprint match forces the R2 skip.
	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		ignoreCursor:    true,
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(clientHad)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.deliveredFirstEventFP = sseEventFingerprint([]byte(event1))
	inf.resp = &host.HostResponse{Receipt: []byte("r")}
	rg.setWinner(inf.nonce)

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))
	require.Equal(t, full, sink.String())
}

func TestSendOnlyWithCursor_PassesResumeFields(t *testing.T) {
	inner := &reconnectScriptClient{fullBody: []byte("data: [DONE]\n\n"), responseReceipt: []byte("r")}
	kc := &killableClient{inner: inner}
	env := setupTestProxyWithClients(t, []user.HostClient{kc})
	prepared, err := env.session.PrepareInference(defaultParams())
	require.NoError(t, err)

	_, err = env.session.SendOnlyWithCursor(context.Background(), prepared, io.Discard, nil, 3, 7)
	require.NoError(t, err)
	req := kc.LastRequest()
	require.NotNil(t, req)
	require.Equal(t, int64(3), req.DeliveredEvents)
	require.Equal(t, int64(7), req.DeliveredPartial)
}

func TestReconnectInflight_ReusesPreparedInference(t *testing.T) {
	client := &reconnectScriptClient{
		fullBody:        []byte(`data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n" + "data: [DONE]\n\n"),
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithClients(t, []user.HostClient{client})
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.Nonce()

	inf := newDoneInflight(prepared, 0, 0)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	rg.setWinner(inf.nonce)
	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil))

	require.Equal(t, nonce, client.lastReq.Nonce)
	require.Equal(t, prepared, inf.prepared, "must reuse the same PreparedInference")
	// A second PrepareInference would allocate nonce+1; reconnect must not.
	next, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	require.Equal(t, nonce+1, next.Nonce())
}

// --- Step 4: protocol gate, reservation, reconnect-first ladder ---

func TestAttemptReconnectAllowed_RequiresSettingAndV5(t *testing.T) {
	env := setupTestProxyWithProtocol(t, []user.HostClient{&reconnectScriptClient{fullBody: []byte("data: [DONE]\n\n")}}, "v4")
	enableAttemptReconnectForTest(t, time.Second, 2)
	require.False(t, env.proxy.redundancy.attemptReconnectAllowed(), "v4 must stay gated off")

	envV5 := setupTestProxyWithProtocol(t, []user.HostClient{&reconnectScriptClient{fullBody: []byte("data: [DONE]\n\n")}}, "v5")
	require.True(t, envV5.proxy.redundancy.attemptReconnectAllowed())

	AttemptReconnectEnabled = false
	require.False(t, envV5.proxy.redundancy.attemptReconnectAllowed(), "setting off disables even on v5")
}

func TestRaceGroup_ReservationBlocksOtherWinner(t *testing.T) {
	rg := newRaceGroup(context.Background(), context.Background(), "escrow", io.Discard)
	rg.setWinner(1)
	rg.reserveWinner(1)
	rg.setWinner(2)
	require.Equal(t, uint64(1), rg.winnerNonce())
	require.True(t, rg.hasReservation())
	rg.clearReservation()
	require.False(t, rg.hasReservation())
	// Winner stays A after clear; B still cannot steal an already-decided race.
	rg.setWinner(2)
	require.Equal(t, uint64(1), rg.winnerNonce())
}

func TestRaceGroup_SecondaryAheadStaysSuppressedUnderReservation(t *testing.T) {
	var sink bytes.Buffer
	rg := newRaceGroup(context.Background(), context.Background(), "escrow", &sink)

	winner := mkRaceWriterInflight(t)
	winner.nonce = 1
	winner.deliveredEvents = 1
	loser := mkRaceWriterInflight(t)
	loser.nonce = 2

	rwA := &raceWriter{group: rg, nonce: 1, inf: winner}
	rwB := &raceWriter{group: rg, nonce: 2, inf: loser}

	contentA := []byte(`data: {"choices":[{"delta":{"content":"from-A"}}]}` + "\n")
	_, err := rwA.Write(contentA)
	require.NoError(t, err)
	require.Equal(t, uint64(1), rg.winnerNonce())
	rg.reserveWinner(1)

	// B produces more content while A is reserved/slow — must not reach client.
	for i := 0; i < 3; i++ {
		_, err = rwB.Write([]byte(`data: {"choices":[{"delta":{"content":"from-B"}}]}` + "\n"))
		require.NoError(t, err)
	}
	require.Equal(t, uint64(1), rg.winnerNonce())
	require.Greater(t, loser.contentChunks.Load(), winner.contentChunks.Load())
	require.NotContains(t, sink.String(), "from-B")
	require.Contains(t, sink.String(), "from-A")

	_, err = rwA.Write([]byte(`data: {"choices":[{"delta":{"content":"more-A"}}]}` + "\n"))
	require.NoError(t, err)
	require.Contains(t, sink.String(), "more-A")
	require.NotContains(t, sink.String(), "from-B")
}

func TestShouldAttemptWinnerReconnect_OnceAndNeedsPrefix(t *testing.T) {
	env := setupTestProxyWithProtocol(t, []user.HostClient{&reconnectScriptClient{fullBody: []byte("data: [DONE]\n\n")}}, "v5")
	enableAttemptReconnectForTest(t, time.Second, 2)
	inf := &inflight{}
	require.False(t, env.proxy.redundancy.shouldAttemptWinnerReconnect(inf), "no delivered prefix")

	inf.deliveredEvents = 1
	require.True(t, env.proxy.redundancy.shouldAttemptWinnerReconnect(inf))
	require.False(t, env.proxy.redundancy.shouldAttemptWinnerReconnect(inf), "ladder once only")
}

func TestRunWinnerReconnectLadder_ResumesAndKeepsClientOnWinner(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"
	clientHad := event1 + "\n\n" + event2[:10]

	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		expectEvents:    1,
		expectPartial:   10,
		responseReceipt: []byte("receipt"),
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	enableAttemptReconnectForTest(t, 200*time.Millisecond, 2)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	var sink bytes.Buffer
	sink.WriteString(clientHad)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 10)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt")}
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true) // ladder entry already claimed by shouldAttempt*

	attempts := []*inflight{inf}
	tried := map[string]bool{env.session.HostParticipantKey(inf.hostIdx): true}
	doneCh := make(chan *inflight, 2)
	err = env.proxy.redundancy.runWinnerReconnectLadderSync(
		context.Background(), context.Background(), inf, rg, params,
		&attempts, tried, nil, doneCh,
	)
	require.NoError(t, err)
	require.Equal(t, full, sink.String())
	require.Equal(t, uint64(inf.nonce), rg.winnerNonce())
	require.False(t, rg.hasReservation(), "reservation cleared after ladder returns")
	require.Len(t, attempts, 1, "fast resume within budget must not start a hedge")
}

func TestRunWinnerReconnectLadder_HedgeStartsWhileReconnectInFlight(t *testing.T) {
	// R3: at budget expiry start secondary while same-nonce reconnect continues.
	// Nonce 1 → hostIdx 1 = slow resume; host 0 = hedge.
	hedgeEntered := make(chan struct{})
	hedge := &signalOnSendClient{
		entered:         hedgeEntered,
		fullBody:        []byte(`data: {"choices":[{"delta":{"content":"HEDGE"}}]}` + "\n\n" + "data: [DONE]\n\n"),
		responseReceipt: []byte("h"),
	}
	releaseResume := make(chan struct{})
	resumeEntered := make(chan struct{})
	resuming := &blockThenResumeClient{
		entered: resumeEntered,
		release: releaseResume,
		fullBody: []byte(
			`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n" +
				`data: {"choices":[{"delta":{"content":"lo"}}]}` + "\n\n" +
				"data: [DONE]\n\n",
		),
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{hedge, resuming}, "v5")
	enableAttemptReconnectForTest(t, 40*time.Millisecond, 1)
	savedBackoff := reconnectTryBackoff
	reconnectTryBackoff = 0
	t.Cleanup(func() { reconnectTryBackoff = savedBackoff })

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	require.Equal(t, 1, prepared.HostIdx())

	var sink bytes.Buffer
	sink.WriteString(`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n")
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.err = errSimulatedWinnerTransport
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true)

	attempts := []*inflight{inf}
	tried := map[string]bool{env.session.HostParticipantKey(1): true}
	doneCh := make(chan *inflight, 4)

	ladderDone := make(chan error, 1)
	go func() {
		ladderDone <- env.proxy.redundancy.runWinnerReconnectLadderSync(
			context.Background(), context.Background(), inf, rg, params,
			&attempts, tried, nil, doneCh,
		)
	}()

	select {
	case <-resumeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("same-nonce reconnect never started")
	}
	// Hedge must start while reconnect is still blocked (R3 parallel race).
	select {
	case <-hedgeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("hedge did not start while reconnect still in flight")
	}
	require.GreaterOrEqual(t, len(attempts), 2, "hedge attempt appended during in-flight reconnect")
	require.NotContains(t, sink.String(), "HEDGE", "hedge must stay off client during reconnect")

	close(releaseResume)
	require.NoError(t, <-ladderDone)
	require.Equal(t, uint64(inf.nonce), rg.winnerNonce(), "winner stays on resumed A")
	require.Contains(t, sink.String(), `"content":"lo"`)
	require.NotContains(t, sink.String(), "HEDGE")
}

func TestRunWinnerReconnectLadder_ExhaustedDoesNotPromoteHedgeToClient(t *testing.T) {
	// Nonce 1 with 2 hosts → hostIdx 1.
	hedge := &reconnectScriptClient{
		fullBody:        []byte(`data: {"choices":[{"delta":{"content":"HEDGE"}}]}` + "\n\n" + "data: [DONE]\n\n"),
		responseReceipt: []byte("h"),
	}
	failing := &countingFailAfterContentClient{}
	env := setupTestProxyWithProtocol(t, []user.HostClient{hedge, failing}, "v5")
	enableAttemptReconnectForTest(t, 30*time.Millisecond, 2)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	require.Equal(t, 1, prepared.HostIdx())

	var sink bytes.Buffer
	sink.WriteString(`data: {"choices":[{"delta":{"content":"x"}}]}` + "\n\n")
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.err = errSimulatedWinnerTransport
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true)

	attempts := []*inflight{inf}
	tried := map[string]bool{env.session.HostParticipantKey(1): true}
	doneCh := make(chan *inflight, 4)
	err = env.proxy.redundancy.runWinnerReconnectLadderSync(
		context.Background(), context.Background(), inf, rg, params,
		&attempts, tried, nil, doneCh,
	)
	require.Error(t, err)
	require.Equal(t, uint64(inf.nonce), rg.winnerNonce(), "hedge must not become client winner")
	require.NotContains(t, sink.String(), "HEDGE")
	require.GreaterOrEqual(t, failing.calls.Load(), int64(2), "same-nonce reconnect tries")
	if len(attempts) > 1 {
		require.NotEqual(t, attempts[1].nonce, rg.winnerNonce())
	}
}

func TestRunInference_V4SkipsReconnectLadderWhenSettingEnabled(t *testing.T) {
	zeroReceiptTimeout(t)
	// Keep a pending secondary so awaitRace takes the doneCh winner-failure path
	// (single-host all-done short-circuits into finishRaceOutcome).
	failing := &countingFailAfterContentClient{}
	releaseSlow := make(chan struct{})
	slow := &releaseAfterClient{releaseCh: releaseSlow}
	env := setupTestProxyWithProtocol(t, []user.HostClient{slow, failing}, "v4")
	enableAttemptReconnectForTest(t, time.Second, 2)
	for i := range 2 {
		env.proxy.redundancy.perf.Record(RequestSample{HostIdx: i, Responsive: false})
	}

	params := defaultParams()
	params.Stream = true
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)
	require.ErrorIs(t, err, errSimulatedWinnerTransport)
	require.Equal(t, int64(1), failing.calls.Load(), "v4 must not same-nonce resend")
	require.Contains(t, buf.String(), `"content":"x"`)
	close(releaseSlow)
}

func TestRunInference_V5ReconnectLadderRetriesSameNonce(t *testing.T) {
	zeroReceiptTimeout(t)
	failing := &countingFailAfterContentClient{}
	releaseSlow := make(chan struct{})
	slow := &releaseAfterClient{releaseCh: releaseSlow}
	// Nonce 1 → host 1.
	env := setupTestProxyWithProtocol(t, []user.HostClient{slow, failing}, "v5")
	enableAttemptReconnectForTest(t, 40*time.Millisecond, 2)
	for i := range 2 {
		env.proxy.redundancy.perf.Record(RequestSample{HostIdx: i, Responsive: false})
	}

	params := defaultParams()
	params.Stream = true
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)
	require.Error(t, err)
	require.GreaterOrEqual(t, failing.calls.Load(), int64(3), "initial send + 2 reconnect tries")
	require.Contains(t, buf.String(), `"content":"x"`)
	require.NotContains(t, buf.String(), "HEDGE")
	close(releaseSlow)
}

func TestReconnectInflight_AssignsCancelAndHonorsIt(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	client := &blockThenResumeClient{
		entered:         entered,
		release:         release,
		fullBody:        []byte("data: [DONE]\n\n"),
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	inf := newDoneInflight(prepared, 1, 0)
	inf.err = errSimulatedWinnerTransport
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	rg.setWinner(inf.nonce)

	errCh := make(chan error, 1)
	go func() {
		errCh <- env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, nil)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect never entered host Send")
	}
	// Reconnect must republish its cancel so hard-timeout can unwind the resume.
	inf.cancelAttempt()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unwind resumed SendOnlyWithCursor")
	}
}

func TestReconnectInflight_ClientDisconnectUsesMetaDrain(t *testing.T) {
	saved := metaDrainTimeout
	metaDrainTimeout = 30 * time.Millisecond
	t.Cleanup(func() { metaDrainTimeout = saved })

	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	client := &blockThenResumeClient{
		entered:         entered,
		release:         release,
		fullBody:        []byte("data: [DONE]\n\n"),
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	inf := newDoneInflight(prepared, 1, 0)
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	rg.setWinner(inf.nonce)
	flag := newCancelFlag()

	errCh := make(chan error, 1)
	started := time.Now()
	go func() {
		errCh <- env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params, flag)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect never entered host Send")
	}
	flag.Trigger()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
		require.GreaterOrEqual(t, time.Since(started), metaDrainTimeout,
			"client disconnect must wait meta-drain before cutting resumed stream")
	case <-time.After(2 * time.Second):
		t.Fatal("meta-drain did not cancel resumed stream")
	}
}

func TestSpawnWinnerReconnectLadder_KeepsAwaitRaceResponsive(t *testing.T) {
	// Ladder must not block awaitRace: while resume is in flight, hard-timeout
	// must still be able to cancel via the reassigned inf.cancel.
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	client := &blockThenResumeClient{
		entered:         entered,
		release:         release,
		fullBody:        []byte(`data: {"choices":[{"delta":{"content":"lo"}}]}` + "\n\n" + "data: [DONE]\n\n"),
		responseReceipt: []byte("r"),
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	enableAttemptReconnectForTest(t, 20*time.Millisecond, 1)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)

	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	inf := newDoneInflight(prepared, 1, 0)
	inf.err = errSimulatedWinnerTransport
	inf.contentChunks.Store(1)
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true)

	doneCh := make(chan *inflight, 2)
	hedgeCh := make(chan winnerReconnectHedgeRequest, 1)
	// Drain hedge requests so the ladder cannot block forever if budget fires
	// while this unit test is not running a full awaitRace select loop.
	go func() {
		for req := range hedgeCh {
			if req.done != nil {
				close(req.done)
			}
		}
	}()
	env.proxy.redundancy.spawnWinnerReconnectLadder(
		context.Background(), context.Background(), inf, rg, params,
		map[string]bool{}, nil, doneCh, hedgeCh,
	)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("spawned ladder never started reconnect")
	}
	require.False(t, inflightDone(inf), "resuming attempt must not look done to the race loop")

	// Simulate awaitRace's hard-timeout branch while ladder is still running.
	inf.cancelAttempt()
	select {
	case got := <-doneCh:
		require.Equal(t, inf, got)
		require.Error(t, inf.err)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled resume did not signal doneCh")
	}
	// The ladder goroutine must be fully unwound before finalizers run.
	settled := make(chan struct{})
	go func() {
		waitInflightSettled(inf)
		close(settled)
	}()
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("waitInflightSettled did not release after the ladder exited")
	}
	require.True(t, inflightDone(inf), "attempt is done once the ladder exits")
}

func TestRunWinnerReconnectLadder_RecordsBlipOnExitNotFailureSample(t *testing.T) {
	client := &countingFailAfterContentClient{}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	enableAttemptReconnectForTest(t, 20*time.Millisecond, 1)
	env.proxy.redundancy.participantLimiter = NewParticipantRequestLimiter(10, 10)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	key := env.session.HostParticipantKey(prepared.HostIdx())
	inf := newDoneInflight(prepared, 1, 0)
	inf.err = errSimulatedWinnerTransport
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", io.Discard)
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true)

	require.Equal(t, 0, env.proxy.redundancy.perf.ReconnectBlipCount(key), "no blip before ladder runs")

	attempts := []*inflight{inf}
	tried := map[string]bool{key: true}
	_ = env.proxy.redundancy.runWinnerReconnectLadderSync(
		context.Background(), context.Background(), inf, rg, params,
		&attempts, tried, nil, make(chan *inflight, 2),
	)

	require.Equal(t, 1, env.proxy.redundancy.perf.ReconnectBlipCount(key), "exhausted ladder records one blip")
	stats := env.proxy.redundancy.perf.StatsForParticipant(key)
	require.Zero(t, stats.FailureSamples, "blip must not be a Responsive:false sample")
	require.False(t, env.proxy.redundancy.participantLimiter.IsShadowQuarantined(key),
		"blips alone must not shadow-quarantine")
}

func TestRunWinnerReconnectLadder_SuccessfulResumeRecordsBlipOnce(t *testing.T) {
	event1 := `data: {"choices":[{"delta":{"content":"Hel"}}]}`
	event2 := `data: {"choices":[{"delta":{"content":"lo"}}]}`
	full := event1 + "\n\n" + event2 + "\n\n" + "data: [DONE]\n\n"
	client := &reconnectScriptClient{
		fullBody:        []byte(full),
		expectEvents:    1,
		expectPartial:   0,
		responseReceipt: []byte("receipt"),
		includeFinish:   true,
	}
	env := setupTestProxyWithProtocol(t, []user.HostClient{client}, "v5")
	enableAttemptReconnectForTest(t, 200*time.Millisecond, 2)
	env.proxy.redundancy.participantLimiter = NewParticipantRequestLimiter(10, 10)

	params := defaultParams()
	params.Stream = true
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	key := env.session.HostParticipantKey(prepared.HostIdx())

	var sink bytes.Buffer
	sink.WriteString(event1 + "\n\n")
	rg := newRaceGroup(context.Background(), context.Background(), "escrow-proxy", &sink)
	inf := newDoneInflight(prepared, 1, 0)
	inf.contentChunks.Store(1)
	inf.err = transport.ErrSSEStreamTruncated
	inf.resp = &host.HostResponse{Receipt: []byte("receipt")}
	rg.setWinner(inf.nonce)
	inf.reconnectLadderUsed.Store(true)

	attempts := []*inflight{inf}
	require.NoError(t, env.proxy.redundancy.runWinnerReconnectLadderSync(
		context.Background(), context.Background(), inf, rg, params,
		&attempts, map[string]bool{key: true}, nil, make(chan *inflight, 2),
	))
	// Call again would be a second ladder; reconnectPenaltyOnce already fired.
	env.proxy.redundancy.recordReconnectBlipOnce(inf)
	require.Equal(t, 1, env.proxy.redundancy.perf.ReconnectBlipCount(key), "one ladder → one blip")
	require.Zero(t, env.proxy.redundancy.perf.StatsForParticipant(key).FailureSamples)
	require.False(t, env.proxy.redundancy.participantLimiter.IsShadowQuarantined(key))
}

func TestReconnectBlips_TwoDoNotShadowQuarantine(t *testing.T) {
	env := setupTestProxyWithProtocol(t, []user.HostClient{
		&reconnectScriptClient{fullBody: []byte("data: [DONE]\n\n"), responseReceipt: []byte("r")},
	}, "v5")
	limiter := NewParticipantRequestLimiter(10, 10)
	env.proxy.redundancy.participantLimiter = limiter
	key := env.session.HostParticipantKey(0)

	env.proxy.redundancy.perf.RecordReconnectBlip(key)
	env.proxy.redundancy.perf.RecordReconnectBlip(key)
	require.Equal(t, 2, env.proxy.redundancy.perf.ReconnectBlipCount(key))
	require.False(t, limiter.IsShadowQuarantined(key))
	require.False(t, env.proxy.redundancy.perf.ParticipantFailureThresholdExceeded(key))
}

// --- Step 3/4 helpers ---

func enableAttemptReconnectForTest(t *testing.T, budget time.Duration, maxTries int) {
	t.Helper()
	savedEnabled := AttemptReconnectEnabled
	savedBudget := AttemptReconnectBudget
	savedTries := AttemptReconnectMaxTries
	AttemptReconnectEnabled = true
	AttemptReconnectBudget = budget
	AttemptReconnectMaxTries = maxTries
	t.Cleanup(func() {
		AttemptReconnectEnabled = savedEnabled
		AttemptReconnectBudget = savedBudget
		AttemptReconnectMaxTries = savedTries
	})
}

func setupTestProxyWithProtocol(t *testing.T, clients []user.HostClient, protocol string) *testProxyEnv {
	t.Helper()
	numHosts := len(clients)
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := types.SessionConfig{
		RefusalTimeout:   1,
		ExecutionTimeout: 1,
		TokenPrice:       1,
		VoteThreshold:    uint32(numHosts) / 2,
	}
	verifier := signing.NewSecp256k1Verifier()
	userSM := statetest.MustStateMachine(t, "escrow-proxy", config, group, 1_000_000, userKey.Address(), verifier,
		state.WithVersion(protocol),
	)
	session, err := user.NewSession(userSM, userKey, "escrow-proxy", group, clients, verifier)
	require.NoError(t, err)
	require.Equal(t, protocol, session.ProtocolVersion())

	perf := NewPerfTracker(nil)
	redundancy := NewRedundancy(session, perf, numHosts, "llama")
	t.Cleanup(redundancy.Stop)

	p := &Proxy{
		session:    session,
		sm:         userSM,
		escrowID:   "escrow-proxy",
		model:      "llama",
		redundancy: redundancy,
		perf:       perf,
	}
	return &testProxyEnv{
		proxy:   p,
		session: session,
		sm:      userSM,
		group:   group,
	}
}

type countingFailAfterContentClient struct {
	calls atomic.Int64
}

func (c *countingFailAfterContentClient) Send(_ context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.calls.Add(1)
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil {
		_, _ = io.WriteString(stream, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}
	return nil, errSimulatedWinnerTransport
}

// blockThenResumeClient blocks in Send until release is closed, then writes fullBody.
// Used to prove the R3 hedge starts while same-nonce reconnect is still in flight.
type blockThenResumeClient struct {
	entered         chan struct{}
	release         chan struct{}
	fullBody        []byte
	responseReceipt []byte
	enterOnce       sync.Once
}

func (c *blockThenResumeClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.enterOnce.Do(func() {
		if c.entered != nil {
			close(c.entered)
		}
	})
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if receiptHandler != nil {
		receiptHandler()
	}
	body := c.fullBody
	if req.DeliveredEvents > 0 || req.DeliveredPartial > 0 {
		var err error
		body, err = skipSSEPrefix(c.fullBody, req.DeliveredEvents, req.DeliveredPartial)
		if err != nil {
			return nil, err
		}
	}
	if stream != nil && len(body) > 0 {
		if _, err := stream.Write(body); err != nil {
			return nil, err
		}
	}
	return &host.HostResponse{
		Nonce:   req.Nonce,
		Receipt: append([]byte(nil), c.responseReceipt...),
	}, nil
}

type signalOnSendClient struct {
	entered         chan struct{}
	fullBody        []byte
	responseReceipt []byte
	enterOnce       sync.Once
}

func (c *signalOnSendClient) Send(_ context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.enterOnce.Do(func() {
		if c.entered != nil {
			close(c.entered)
		}
	})
	if receiptHandler != nil {
		receiptHandler()
	}
	if stream != nil && len(c.fullBody) > 0 {
		_, _ = stream.Write(c.fullBody)
	}
	return &host.HostResponse{
		Nonce:   req.Nonce,
		Receipt: append([]byte(nil), c.responseReceipt...),
	}, nil
}

func newDoneInflight(prepared *user.PreparedInference, events, partial int64) *inflight {
	done := make(chan struct{})
	close(done)
	inf := &inflight{
		prepared:         prepared,
		hostIdx:          prepared.HostIdx(),
		hostID:           "host-0",
		nonce:            prepared.Nonce(),
		escrowID:         "escrow-proxy",
		sendTime:         time.Now(),
		done:             done,
		receiptCh:        make(chan struct{}),
		firstTokenCh:     make(chan struct{}),
		deliveredEvents:  events,
		deliveredPartial: partial,
	}
	close(inf.receiptCh)
	inf.receiptOnce.Do(func() {})
	inf.setReceiptAt(time.Now())
	return inf
}

type reconnectScriptClient struct {
	mu              sync.Mutex
	calls           int
	fullBody        []byte
	expectEvents    int64
	expectPartial   int64
	ignoreCursor    bool
	responseReceipt []byte
	// includeFinish adds MsgFinishInference for req.Nonce to the HostResponse
	// mempool (simulates host devshard_meta after a successful resume).
	includeFinish bool
	lastReq       host.HostRequest
}

func finishInferenceTx(nonce uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: nonce},
	}}
}

func (c *reconnectScriptClient) Send(_ context.Context, req host.HostRequest, stream io.Writer, receiptHandler func()) (*host.HostResponse, error) {
	c.mu.Lock()
	c.calls++
	c.lastReq = req
	c.mu.Unlock()
	if receiptHandler != nil {
		receiptHandler()
	}
	if !c.ignoreCursor {
		if c.expectEvents != 0 || c.expectPartial != 0 {
			if req.DeliveredEvents != c.expectEvents || req.DeliveredPartial != c.expectPartial {
				return nil, fmt.Errorf("cursor mismatch: got (%d,%d) want (%d,%d)",
					req.DeliveredEvents, req.DeliveredPartial, c.expectEvents, c.expectPartial)
			}
		}
	}
	body := c.fullBody
	if !c.ignoreCursor && (req.DeliveredEvents > 0 || req.DeliveredPartial > 0) {
		var err error
		body, err = skipSSEPrefix(c.fullBody, req.DeliveredEvents, req.DeliveredPartial)
		if err != nil {
			return nil, err
		}
	}
	if stream != nil && len(body) > 0 {
		if _, err := stream.Write(body); err != nil {
			return nil, err
		}
	}
	var mempool []*types.DevshardTx
	if c.includeFinish {
		mempool = []*types.DevshardTx{finishInferenceTx(req.Nonce)}
	}
	return &host.HostResponse{
		Nonce:            req.Nonce,
		Receipt:          append([]byte(nil), c.responseReceipt...),
		ConfirmedAt:      11,
		Mempool:          mempool,
		DeliveredEvents:  req.DeliveredEvents,
		DeliveredPartial: req.DeliveredPartial,
	}, nil
}

func skipSSEPrefix(body []byte, events, partial int64) ([]byte, error) {
	buf := &bytes.Buffer{}
	sw := &prefixSkipWriter{w: buf, skipEvents: events, skipPartial: partial}
	if _, err := sw.Write(body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

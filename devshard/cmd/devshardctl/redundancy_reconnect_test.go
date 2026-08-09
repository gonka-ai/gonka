package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
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

	err = env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params)
	require.NoError(t, err)
	require.Equal(t, int64(1), inf.reconnectTries.Load())
	require.NoError(t, inf.err)
	require.Equal(t, deliveredEvents, client.lastReq.DeliveredEvents)
	require.Equal(t, deliveredPartial, client.lastReq.DeliveredPartial)
	require.Equal(t, prepared.Nonce(), client.lastReq.Nonce)
	require.Equal(t, full, sink.String(), "client-visible stream must be continuous with no gap/dup")
	require.Equal(t, []byte("receipt-1"), inf.resp.Receipt)
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

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params))
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
	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params))
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
	inf.resp = &host.HostResponse{Receipt: []byte("r")}
	rg.setWinner(inf.nonce)

	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params))
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
	require.NoError(t, env.proxy.redundancy.reconnectInflight(context.Background(), inf, rg, params))

	require.Equal(t, nonce, client.lastReq.Nonce)
	require.Equal(t, prepared, inf.prepared, "must reuse the same PreparedInference")
	// A second PrepareInference would allocate nonce+1; reconnect must not.
	next, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	require.Equal(t, nonce+1, next.Nonce())
}

// --- Step 3 helpers ---

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
	lastReq         host.HostRequest
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
	return &host.HostResponse{
		Nonce:            req.Nonce,
		Receipt:          append([]byte(nil), c.responseReceipt...),
		ConfirmedAt:      11,
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

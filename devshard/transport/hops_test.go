package transport

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

type captureHopObserver struct {
	reqMs   int64
	chunks  [][3]int64 // ml, w, recv
	tiers   []string
	absent  int
}

func (o *captureHopObserver) OnReqMs(reqMs int64) { o.reqMs = reqMs }
func (o *captureHopObserver) OnChunk(tier string, mlMs, wMs, recvMs int64) {
	o.tiers = append(o.tiers, tier)
	o.chunks = append(o.chunks, [3]int64{mlMs, wMs, recvMs})
}
func (o *captureHopObserver) OnChunkAbsent() { o.absent++ }

func TestHandleSSELine_DevshardTSNotForwarded(t *testing.T) {
	c := &HTTPClient{config: DefaultClientConfig()}
	var out bytes.Buffer
	var result host.HostResponse
	var writeErrLogged, unexpectedLineLogged, sawTerminator bool
	obs := &captureHopObserver{}
	hops := hopParseState{obs: obs}

	comment := host.FormatDevshardTSComment(0, []int64{100}, []int64{110}, host.HopTierLive)
	c.handleSSELine(comment, &out, nil, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator, &hops)
	require.Empty(t, out.String(), "comments must not be forwarded")
	require.False(t, unexpectedLineLogged)

	c.handleSSELine(`data: {"choices":[{"delta":{"content":"x"}}]}`, &out, nil, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator, &hops)
	require.Contains(t, out.String(), `data: {"choices"`)
	require.Len(t, obs.chunks, 1)
	require.Equal(t, int64(100), obs.chunks[0][0])
	require.Equal(t, int64(110), obs.chunks[0][1])
	require.Equal(t, 0, obs.absent)
}

func TestHandleSSELine_ReqMsOnReceipt(t *testing.T) {
	c := &HTTPClient{config: DefaultClientConfig()}
	var result host.HostResponse
	var writeErrLogged, unexpectedLineLogged, sawTerminator bool
	obs := &captureHopObserver{}
	hops := hopParseState{obs: obs}
	line := `data: {"devshard_receipt":{"nonce":1,"req_ms":12345,"confirmed_at":9}}`
	c.handleSSELine(line, io.Discard, nil, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator, &hops)
	require.True(t, sawTerminator)
	require.Equal(t, int64(12345), result.ReqMs)
	require.Equal(t, int64(12345), obs.reqMs)
}

func TestHandleSSELine_MalformedCommentIgnored(t *testing.T) {
	c := &HTTPClient{config: DefaultClientConfig()}
	var out bytes.Buffer
	var result host.HostResponse
	var writeErrLogged, unexpectedLineLogged, sawTerminator bool
	obs := &captureHopObserver{}
	hops := hopParseState{obs: obs}

	c.handleSSELine(": devshard-ts {not-json", &out, nil, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator, &hops)
	c.handleSSELine(`data: {"x":1}`, &out, nil, &result, &writeErrLogged, &unexpectedLineLogged, &sawTerminator, &hops)
	require.Equal(t, 1, obs.absent)
	require.Empty(t, obs.chunks)
	require.False(t, unexpectedLineLogged)
}

func TestParseSSEResponse_ContextHopObserver(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(`data: {"devshard_receipt":{"nonce":1,"req_ms":1000}}` + "\n\n")
	buf.WriteString(host.FormatDevshardTSComment(0, []int64{1100}, []int64{1200}, host.HopTierCache) + "\n")
	buf.WriteString(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	buf.WriteString("data: [DONE]\n\n")

	obs := &captureHopObserver{}
	ctx := ContextWithHopObserver(context.Background(), obs)
	c := &HTTPClient{config: DefaultClientConfig()}
	var out bytes.Buffer
	resp, err := c.parseSSEResponse(ctx, bytes.NewReader(buf.Bytes()), &out, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1000), resp.ReqMs)
	require.Equal(t, int64(1000), obs.reqMs)
	require.Len(t, obs.chunks, 1)
	require.Equal(t, host.HopTierCache, obs.tiers[0])
	require.NotContains(t, out.String(), "devshard-ts")
}

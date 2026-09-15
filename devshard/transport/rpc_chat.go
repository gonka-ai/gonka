package transport

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"

	"devshard/host"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
)

// Send implements user.HostClient. Opted-in Chat uses Connect frames that
// concatenate into one gzip stream, then the existing SSE parser.
func (c *RPCClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if !c.Uses(EndpointChat) || c.sessionChat == nil {
		return c.HTTPClient.Send(ctx, req, stream, receiptHandler)
	}
	timeout := c.config.InferenceTimeout
	if req.Payload == nil {
		timeout = c.config.QueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Attach is async with SelectTransport / Start. GetPayload already waits;
	// Chat must not take budget or a stream slot on a token miss.
	if err := c.WaitReady(ctx); err != nil {
		return nil, err
	}

	ir, err := HostRequestToJSON(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	body, _, outboundHS, err := c.wrapInferenceRequest(ctx, req, ir)
	if err != nil {
		return nil, err
	}
	env, err := c.signEnvelope(body)
	if err != nil {
		return nil, err
	}
	if err := c.conn.takePeerBudget(ctx, rpcpbconnect.SessionServiceChatProcedure); err != nil {
		return nil, err
	}
	if !c.conn.acquireChatStream() {
		c.conn.refundPeerBudget(rpcpbconnect.SessionServiceChatProcedure)
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many concurrent streams"))
	}
	defer c.conn.releaseChatStream()

	creq, err := tokenRequest(c, env)
	if err != nil {
		c.conn.refundPeerBudget(rpcpbconnect.SessionServiceChatProcedure)
		return nil, err
	}
	cs, err := c.sessionChat.Chat(ctx, creq)
	if err != nil {
		if isQuotaResourceExhausted(err) {
			c.conn.refundPeerBudget(rpcpbconnect.SessionServiceChatProcedure)
		}
		c.observeTransportFailure("/sessions/"+c.escrowID+"/chat/completions", err)
		return nil, err
	}
	defer func() { _ = cs.Close() }()
	if outboundHS != nil {
		c.markHeightSyncPropagated(outboundHS)
	}
	result, err := c.parseChatStream(ctx, cs, stream, receiptHandler)
	if err != nil && !errors.Is(err, ErrSSEStreamTruncated) && !errors.Is(err, ErrSSEEventTooLarge) && !errors.Is(err, ErrSSEStreamTooLarge) {
		c.observeTransportFailure("/sessions/"+c.escrowID+"/chat/completions", err)
	}
	return result, err
}

func (c *RPCClient) parseChatStream(ctx context.Context, stream *connect.ServerStreamForClient[rpcpb.ChatFrame], streamWriter io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	r := &chatFrameReader{stream: stream}
	br := bufio.NewReader(r)
	if _, err := br.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrSSEStreamTruncated
		}
		return nil, err
	}
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("%w: gzip header: %v", ErrSSEStreamTruncated, err)
	}
	defer gz.Close()
	cr := &countingReader{r: gz}
	result, err := c.parseSSEResponse(ctx, cr, streamWriter, receiptHandler)
	if result != nil {
		result.StreamBytesRead = cr.n
	}
	if err != nil && result != nil {
		return result, err
	}
	return result, err
}

type chatFrameReader struct {
	stream *connect.ServerStreamForClient[rpcpb.ChatFrame]
	buf    []byte
	err    error
}

func (r *chatFrameReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.stream == nil || !r.stream.Receive() {
			if r.stream != nil {
				if err := r.stream.Err(); err != nil {
					r.err = err
					return 0, err
				}
			}
			r.err = io.EOF
			return 0, io.EOF
		}
		r.buf = append([]byte(nil), r.stream.Msg().GetChunk()...)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

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
	if !c.Uses(EndpointChat) || c.sessionChatClient() == nil {
		return c.HTTPClient.Send(ctx, req, stream, receiptHandler)
	}
	timeout := c.config.InferenceTimeout
	if req.Payload == nil {
		timeout = c.config.QueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Attach is async with SelectTransport / Start. A token-race Unauthenticated
	// retries once after WaitReady, before that attempt takes budget.
	path := "/sessions/" + c.escrowID + "/chat/completions"
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.WaitReady(ctx); err != nil {
			return nil, err
		}
		result, err := c.sendChatOnce(ctx, req, stream, receiptHandler, path)
		if attempt == 0 && connect.CodeOf(err) == connect.CodeUnauthenticated {
			c.conn.refundPeerBudget(rpcpbconnect.SessionServiceChatProcedure)
			last = err
			if sleepErr := sleepContext(ctx, unauthenticatedRetryDelay); sleepErr != nil {
				return nil, err
			}
			continue
		}
		return result, err
	}
	return nil, last
}

func (c *RPCClient) sendChatOnce(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse), path string) (*host.HostResponse, error) {
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
	if err := c.allowRequest(path); err != nil {
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
	cs, err := c.sessionChatClient().Chat(ctx, creq)
	if err != nil {
		c.finishChatError(path, err)
		return nil, err
	}
	defer func() { _ = cs.Close() }()
	if outboundHS != nil {
		c.markHeightSyncPropagated(outboundHS)
	}
	result, err := c.parseChatStream(ctx, cs, stream, receiptHandler)
	if err != nil && !errors.Is(err, ErrSSEStreamTruncated) && !errors.Is(err, ErrSSEEventTooLarge) && !errors.Is(err, ErrSSEStreamTooLarge) {
		c.finishChatError(path, err)
	}
	return result, err
}

// finishChatError refunds a server quota rejection (those arrive on the first
// stream Receive, not from Chat) and grades an application status separately
// from a dial, reset, or EOF.
func (c *RPCClient) finishChatError(path string, err error) {
	if isQuotaResourceExhausted(err) && c.conn != nil {
		c.conn.refundPeerBudget(rpcpbconnect.SessionServiceChatProcedure)
	}
	c.observeChat(path, err)
}

func (c *RPCClient) observeChat(path string, err error) {
	if err == nil || c == nil || c.HTTPClient == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return
	}
	status, devshardCode, ok := connectResultStatus(err)
	if !ok {
		c.observeTransportFailure(path, err)
		return
	}
	body := ""
	var ce *connect.Error
	if errors.As(err, &ce) {
		body = ce.Message()
		if len(body) > maxErrorBodyBytes {
			body = body[:maxErrorBodyBytes]
		}
	}
	if shouldObserveUpstreamStatus(path, status, body, devshardCode, "") {
		c.observeResultWithBody(path, status, body, devshardCode, "")
	}
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

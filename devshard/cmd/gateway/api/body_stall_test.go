package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/limits"
)

const (
	exchangeBound        = 10 * time.Second
	shutdownBound        = 5 * time.Second
	stalledBodyDeclared  = 1000
	stalledBodyDelivered = 10
	declaredBeyondLimit  = 8
)

type cancellationAwareLimiter struct{ *countingLimiter }

func (l cancellationAwareLimiter) AcquireForModel(ctx context.Context, model string, inputTokens int64, capacity limits.ModelCapacity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.countingLimiter.AcquireForModel(ctx, model, inputTokens, capacity)
}

type expiringBodyConnection struct {
	net.Conn
	expired chan<- struct{}
}

func (c expiringBodyConnection) SetReadDeadline(deadline time.Time) error {
	if remaining := time.Until(deadline); remaining > readHeaderTimeout && remaining <= bodyReadTimeout {
		deadline = time.Now()
		defer func() {
			select {
			case c.expired <- struct{}{}:
			default:
			}
		}()
	}
	return c.Conn.SetReadDeadline(deadline)
}

type expiringBodyListener struct {
	net.Listener
	expired chan<- struct{}
}

func (l expiringBodyListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return expiringBodyConnection{Conn: connection, expired: l.expired}, nil
}

func serveOverLoopback(t *testing.T, live *harness, expireBodyDeadline bool) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}
	address := listener.Addr().String()
	expired := make(chan struct{}, 1)
	if expireBodyDeadline {
		listener = expiringBodyListener{Listener: listener, expired: expired}
	}
	server, cancelRequests := live.server.HTTPServer(address)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		cancelRequests()
		draining, stopDraining := context.WithTimeout(context.Background(), shutdownBound)
		defer stopDraining()
		if err := server.Shutdown(draining); err != nil {
			_ = server.Close()
		}
		select {
		case <-stopped:
		case <-time.After(shutdownBound):
			t.Errorf("the gateway server did not stop within %v", shutdownBound)
		}
	})
	return address, expired
}

type rawClient struct {
	connection net.Conn
	reader     *bufio.Reader
}

func dialGateway(t *testing.T, address string) *rawClient {
	t.Helper()
	connection, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatalf("dialing the gateway: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connection.SetDeadline(time.Now().Add(exchangeBound)); err != nil {
		t.Fatalf("bounding the exchange: %v", err)
	}
	return &rawClient{connection: connection, reader: bufio.NewReader(connection)}
}

func (c *rawClient) write(t *testing.T, text string) error {
	t.Helper()
	_, err := io.WriteString(c.connection, text)
	return err
}

func (c *rawClient) writeOrFail(t *testing.T, text string) {
	t.Helper()
	if err := c.write(t, text); err != nil {
		t.Fatalf("writing to the gateway: %v", err)
	}
}

type reply struct {
	status  int
	closing bool
	body    string
}

func (c *rawClient) tryReply() (reply, error) {
	response, err := http.ReadResponse(c.reader, nil)
	if err != nil {
		return reply{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return reply{status: response.StatusCode, closing: response.Close, body: string(body)}, err
}

func (c *rawClient) reply(t *testing.T, waitingFor string) reply {
	t.Helper()
	got, err := c.tryReply()
	if err != nil {
		t.Fatalf("waiting for %s: %v", waitingFor, err)
	}
	return got
}

func timedOut(err error) bool {
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func (c *rawClient) closedByServer(t *testing.T) bool {
	t.Helper()
	_, err := c.reader.ReadByte()
	if timedOut(err) {
		t.Fatalf("waiting for the gateway to close the connection after refusing the body: %v", err)
	}
	return err != nil
}

func chatRequestHead(contentLength int) string {
	return fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: gateway\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", clientKey, contentLength)
}

func wholeChatRequest() string {
	return chatRequestHead(len(chatBody)) + chatBody
}

func stallBody(t *testing.T, client *rawClient, declared int) {
	t.Helper()
	client.writeOrFail(t, chatRequestHead(declared)+chatBody[:stalledBodyDelivered])
}

// Test flow:
//  1. Serve the harness over loopback with every body read deadline already expired.
//  2. Send a chat head that declares a long body and deliver only its first bytes.
//  3. Assert the reply is a 400 that announces the connection closes, and names no socket address.
//  4. Assert the server closes the connection after the reply.
//  5. Assert exactly one warning names the request and the cause.
//  6. Assert the stalled body took no limiter slot and started no race.
func TestAStalledBodyIsRefusedAsTheClientsAndTheConnectionCloses(t *testing.T) {
	logged := logcapture.Install(t)
	live := newHarness(t)
	address, _ := serveOverLoopback(t, live, true)
	client := dialGateway(t, address)

	stallBody(t, client, stalledBodyDeclared)
	got := client.reply(t, "the reply to a stalled body")

	if got.status != http.StatusBadRequest {
		t.Fatalf("status: got %d (%s), want 400", got.status, got.body)
	}
	if !got.closing {
		t.Fatal("the reply to a stalled body did not announce that the connection closes")
	}
	if strings.Contains(got.body, "127.0.0.1") {
		t.Fatalf("the reply names a socket address: %s", got.body)
	}
	if !client.closedByServer(t) {
		t.Fatal("the server kept the connection open after a stalled body")
	}
	live.events.Flush()
	var warnings int
	for _, entry := range logged.All() {
		if entry.Msg == "request body not received: connection closed" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("warnings about the unread body: got %d, want 1", warnings)
	}
	entry, _ := logged.Find("request body not received: connection closed")
	if entry.Level != "warn" || logcapture.Field(entry, "request") != "request-1" {
		t.Fatalf("the line about the unread body: got %+v, want a warning for request-1", entry)
	}
	if cause, _ := logcapture.Field(entry, "error").(string); !strings.Contains(cause, "timeout") {
		t.Fatalf("the line about the unread body names the cause %q, want the timeout", cause)
	}
	if got := live.limiter.acquires.Load(); got != 0 {
		t.Fatalf("a stalled body took %d limiter slots", got)
	}
	if got := live.inference.runs.Load(); got != 0 {
		t.Fatalf("a stalled body started %d races", got)
	}
}

// Test flow:
//  1. Serve the harness, whose limiter refuses a cancelled context as the real one does, over loopback with every body read deadline already expired.
//  2. Deliver the first bytes of a chat body, wait until the server has armed its expired deadline, then deliver the rest.
//  3. Read whatever the gateway answers to that request.
//  4. Send a whole chat request on the same connection.
//  5. Assert the gateway either closed that connection or served the request, and never answers it with the earlier request's cancellation.
//  6. Send a whole chat request on a new connection and assert it is served.
func TestARequestAfterAStalledBodyIsNotFailedByItsPredecessor(t *testing.T) {
	live := newHarness(t)
	live.server.limiter = cancellationAwareLimiter{countingLimiter: live.limiter}
	address, deadlineExpired := serveOverLoopback(t, live, true)
	stalled := dialGateway(t, address)
	stallBody(t, stalled, len(chatBody))
	select {
	case <-deadlineExpired:
	case <-time.After(exchangeBound):
		t.Fatalf("waiting for the server to arm a deadline on the body of the stalled request")
	}
	_ = stalled.write(t, chatBody[stalledBodyDelivered:])
	if _, err := stalled.tryReply(); timedOut(err) {
		t.Fatalf("waiting for the reply to a stalled body: %v", err)
	}

	if err := stalled.write(t, wholeChatRequest()); err == nil {
		got, err := stalled.tryReply()
		if timedOut(err) {
			t.Fatalf("waiting for the reply to a request after a stalled body: %v", err)
		}
		if err == nil && got.status != http.StatusOK {
			t.Fatalf("a request after a stalled body: got %d (%s), want 200 or a closed connection", got.status, got.body)
		}
	}

	fresh := dialGateway(t, address)
	fresh.writeOrFail(t, wholeChatRequest())
	got := fresh.reply(t, "the reply on a new connection")
	if got.status != http.StatusOK {
		t.Fatalf("a request on a new connection: got %d (%s), want 200", got.status, got.body)
	}
}

// Test flow:
//  1. Serve the harness over loopback with the real body read deadline.
//  2. Send a chat head and half of its body, then the other half in a second write.
//  3. Assert the reply is 200 and the connection stays open.
//  4. Send a whole chat request on the same connection and assert it is served too.
func TestABodyArrivingInTwoPartsIsServedAndTheConnectionIsKept(t *testing.T) {
	live := newHarness(t)
	address, _ := serveOverLoopback(t, live, false)
	client := dialGateway(t, address)
	half := len(chatBody) / 2

	client.writeOrFail(t, chatRequestHead(len(chatBody))+chatBody[:half])
	client.writeOrFail(t, chatBody[half:])
	first := client.reply(t, "the reply to a body sent in two parts")
	if first.status != http.StatusOK || first.closing {
		t.Fatalf("a body in two parts: got %d close=%v (%s), want 200 on a kept connection", first.status, first.closing, first.body)
	}

	client.writeOrFail(t, wholeChatRequest())
	second := client.reply(t, "the reply to a second request on the same connection")
	if second.status != http.StatusOK {
		t.Fatalf("the second request: got %d (%s), want 200", second.status, second.body)
	}
}

// Test flow:
//  1. Serve the harness over loopback with the real body read deadline.
//  2. Send a chat head that declares a few bytes more than `chatIngestLimit` and deliver one byte more than the limit, then stop.
//  3. Assert the reply is 413.
//  4. Assert the server closes the connection rather than waiting for the rest of the body.
func TestAnOversizedBodyThatStopsShortOfItsDeclaredLengthDoesNotHoldTheConnection(t *testing.T) {
	live := newHarness(t)
	address, _ := serveOverLoopback(t, live, false)
	client := dialGateway(t, address)

	client.writeOrFail(t, chatRequestHead(chatIngestLimit+declaredBeyondLimit)+strings.Repeat("A", chatIngestLimit+1))
	got := client.reply(t, "the reply to an oversized body")

	if got.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d (%s), want 413", got.status, got.body)
	}
	if !client.closedByServer(t) {
		t.Fatal("the server kept the connection open while waiting for the rest of a refused body")
	}
}

package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

const (
	shutdownTestBudget       = 5 * time.Second
	shutdownTestShortRequest = 200 * time.Millisecond
	shutdownTestRequest      = "GET /v1/chat/completions HTTP/1.1\r\nHost: gateway\r\n\r\n"
)

type pipeAddress struct{}

func (pipeAddress) Network() string { return "pipe" }
func (pipeAddress) String() string  { return "pipe" }

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddress{} }

func (l *pipeListener) send(t *testing.T) net.Conn {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	l.connections <- serverSide
	if _, err := io.WriteString(clientSide, shutdownTestRequest); err != nil {
		t.Fatalf("WriteString(request): %v", err)
	}
	return clientSide
}

func readThroughLine(reader *bufio.Reader, want string) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == want {
			return nil
		}
	}
}

// Test flow:
//  1. Build the gateway's http.Server, on fake time, over a handler that writes one SSE chunk and then holds the stream open until its request context ends.
//  2. Serve it on an in-memory listener, open the stream, read the first chunk, and keep reading the rest.
//  3. Start Shutdown with a budget far longer than a prompt shutdown needs, then call the cancel HTTPServer returned.
//  4. Assert Shutdown returns nil inside that budget and the handler saw its request context cancelled.
func TestRequestCancelEndsAStreamThatWouldNeverEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handlerCancelled := make(chan struct{})
		streaming := &Server{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(handlerCancelled)
		})}
		listener := newPipeListener()
		server, endRequests := streaming.HTTPServer("pipe")
		served := make(chan error, 1)
		go func() { served <- server.Serve(listener) }()
		connection := listener.send(t)
		defer connection.Close()
		stream := bufio.NewReader(connection)
		if err := readThroughLine(stream, "data: first\n"); err != nil {
			t.Fatalf("reading the first chunk: %v", err)
		}
		go func() { _, _ = io.Copy(io.Discard, stream) }()

		budget, cancelBudget := context.WithTimeout(t.Context(), shutdownTestBudget)
		defer cancelBudget()
		shutdownErr := make(chan error, 1)
		go func() { shutdownErr <- server.Shutdown(budget) }()
		endRequests()

		if err := <-shutdownErr; err != nil {
			t.Fatalf("Shutdown() = %v, want nil: the open stream held the shutdown for its whole budget", err)
		}
		select {
		case <-handlerCancelled:
		default:
			t.Fatal("the stream's request context was not cancelled by the shutdown")
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() = %v, want http.ErrServerClosed", err)
		}
	})
}

// Test flow:
//  1. Build the gateway's http.Server, on fake time, over a handler that works for a short while, notes whether its request context ended, and then writes its whole body.
//  2. Serve it on an in-memory listener, send one request, and wait until the handler has started.
//  3. Call Shutdown with a budget far longer than the handler needs.
//  4. Assert Shutdown returns nil, the client reads the full body, and the handler's request context was never cancelled.
func TestShutdownLetsAShortRequestFinishUncancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		contextErr := make(chan error, 1)
		short := &Server{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-time.After(shutdownTestShortRequest):
			case <-r.Context().Done():
			}
			contextErr <- r.Context().Err()
			fmt.Fprint(w, "complete answer")
		})}
		listener := newPipeListener()
		server, _ := short.HTTPServer("pipe")
		served := make(chan error, 1)
		go func() { served <- server.Serve(listener) }()
		type reply struct {
			body string
			err  error
		}
		connection := listener.send(t)
		defer connection.Close()
		<-started
		replied := make(chan reply, 1)
		go func() {
			response, err := http.ReadResponse(bufio.NewReader(connection), nil)
			if err != nil {
				replied <- reply{err: err}
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			replied <- reply{body: string(body), err: err}
		}()

		budget, cancelBudget := context.WithTimeout(t.Context(), shutdownTestBudget)
		defer cancelBudget()
		shutdownErr := server.Shutdown(budget)

		if shutdownErr != nil {
			t.Fatalf("Shutdown() = %v, want nil", shutdownErr)
		}
		if err := <-contextErr; err != nil {
			t.Fatalf("the short request's context ended with %v during a graceful shutdown, want it never cancelled", err)
		}
		if got := <-replied; got.err != nil || got.body != "complete answer" {
			t.Fatalf("client read %q, %v; want the complete answer", got.body, got.err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() = %v, want http.ErrServerClosed", err)
		}
	})
}

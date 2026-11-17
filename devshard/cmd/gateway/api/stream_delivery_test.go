package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"devshard/host"
)

type pausedStreamHost struct {
	*flushingHost
	release <-chan struct{}
}

func (h *pausedStreamHost) Send(ctx context.Context, req host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	reply, err := h.flushingHost.Send(ctx, req, stream, onReceipt)
	if err != nil || stream == nil {
		return reply, err
	}
	select {
	case <-h.release:
		return reply, nil
	case <-ctx.Done():
		return reply, ctx.Err()
	}
}

func TestClientReceivesAnEventBeforeDispatchReturns(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	letFinish := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := &pausedStreamHost{flushingHost: &flushingHost{line: "data: {\"choices\":[]}\n\n"}, release: release}
	session, _ := newLiveSession(t, upstream, upstream)
	prepared := prepareNonce(t, session)
	target := escrowTarget{session: session}
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		reply, err := target.Send(r.Context(), prepared, w, nil)
		if reply != nil {
			reply.ReleaseFinish()
		}
		done <- err
	}))
	defer server.Close()
	defer letFinish()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "data: {\"choices\":[]}\n" {
		t.Fatalf("first event=%q", line)
	}
	select {
	case err := <-done:
		t.Fatalf("dispatch finished before release: %v", err)
	default:
	}
	letFinish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

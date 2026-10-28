package engine

import (
	"context"
	"io"
	"net/http"
	"time"

	"devshard/cmd/gateway/config"
)

// defaultDrainTimeout must exceed every deadline a race arms for itself. See race.md, "Client departure and the drain".
const defaultDrainTimeout = 40 * time.Minute

func DrainTimeoutFromConfig(stream config.Stream) time.Duration {
	return time.Duration(stream.DrainTimeoutSeconds) * time.Second
}

// drain holds a race's two contexts; the client's cancellation is deliberately dropped, not inherited. See race.md, "Client departure and the drain".
type drain struct {
	client  context.Context
	race    context.Context
	timeout time.Duration
}

func newDrain(clientCtx context.Context, timeout time.Duration) drain {
	if timeout <= 0 {
		timeout = defaultDrainTimeout
	}
	return drain{
		client:  clientCtx,
		race:    context.WithoutCancel(clientCtx),
		timeout: timeout,
	}
}

func (d drain) clientGone() <-chan struct{} { return d.client.Done() }

func (d drain) clientErr() error { return d.client.Err() }

func (d drain) deadline(clientGoneAt time.Time) time.Time {
	if clientGoneAt.IsZero() {
		return time.Time{}
	}
	return clientGoneAt.Add(d.timeout)
}

func (d drain) gate(client io.Writer) io.Writer {
	if client == nil {
		return nil
	}
	return clientStream{client: client, gone: d.clientGone()}
}

// clientStream reports a departed client's bytes as written instead of failing. See race.md, "Client departure and the drain".
type clientStream struct {
	client io.Writer
	gone   <-chan struct{}
}

func (s clientStream) Write(chunk []byte) (int, error) {
	select {
	case <-s.gone:
		return len(chunk), nil
	default:
	}
	return s.client.Write(chunk)
}

func (s clientStream) Flush() {
	select {
	case <-s.gone:
		return
	default:
	}
	if flusher, ok := s.client.(http.Flusher); ok {
		flusher.Flush()
	}
}

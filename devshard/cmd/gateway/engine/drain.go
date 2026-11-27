package engine

import (
	"context"
	"errors"
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
	depart  context.CancelCauseFunc
	race    context.Context
	timeout time.Duration
}

func newDrain(clientCtx context.Context, timeout time.Duration) drain {
	if timeout <= 0 {
		timeout = defaultDrainTimeout
	}
	client, depart := context.WithCancelCause(clientCtx)
	return drain{
		client:  client,
		depart:  depart,
		race:    context.WithoutCancel(clientCtx),
		timeout: timeout,
	}
}

func (d drain) clientGone() <-chan struct{} { return d.client.Done() }

func (d drain) clientErr() error { return context.Cause(d.client) }

func (d drain) replyRefused() bool { return errors.Is(d.clientErr(), ErrReplyRefused) }

func (d drain) deadline(clientGoneAt time.Time) time.Time {
	if clientGoneAt.IsZero() {
		return time.Time{}
	}
	return clientGoneAt.Add(d.timeout)
}

// DepartedSink is a client writer that keeps taking the winner's bytes after the client left. See race.md, "Client departure and the drain".
type DepartedSink interface {
	WriteDeparted(chunk []byte)
}

func (d drain) gate(client io.Writer) io.Writer {
	if client == nil {
		return nil
	}
	departed, _ := client.(DepartedSink)
	return clientStream{client: client, gone: d.clientGone(), depart: d.depart, departed: departed}
}

type clientStream struct {
	client   io.Writer
	gone     <-chan struct{}
	depart   context.CancelCauseFunc
	departed DepartedSink
}

func (s clientStream) Write(chunk []byte) (int, error) {
	select {
	case <-s.gone:
		if s.departed != nil {
			s.departed.WriteDeparted(chunk)
		}
		return len(chunk), nil
	default:
	}
	if _, err := s.client.Write(chunk); err != nil {
		s.depart(err)
	}
	return len(chunk), nil
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

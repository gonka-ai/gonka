package registry

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"devshard/host"
	"devshard/user"
)

type panickingSender struct{}

func (panickingSender) SendOnly(context.Context, *user.PreparedInference, io.Writer, func()) (*host.HostResponse, error) {
	panic("index out of range in a decoder")
}

// Test flow:
//  1. Send through a sender whose read of the host's reply panics, as a decoder fed malformed bytes can.
//  2. Call SendRecovering on it.
//  3. Assert it returns no reply and ErrSendPanicked carrying the panic: the race and the warmup probe both send through it, so neither can take the process down.
func TestSendRecoveringTurnsAPanicIntoAnError(t *testing.T) {
	reply, err := SendRecovering(context.Background(), panickingSender{}, nil, nil, nil)

	if reply != nil {
		t.Fatalf("reply = %v, want none", reply)
	}
	if !errors.Is(err, ErrSendPanicked) || !strings.Contains(err.Error(), "index out of range in a decoder") {
		t.Fatalf("error = %v, want ErrSendPanicked with the panic", err)
	}
}

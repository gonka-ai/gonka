package gatewayproto

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"devshard/host"
	"devshard/user"
)

type senderFunc func(context.Context, *user.PreparedInference, io.Writer, func()) (*host.HostResponse, error)

func (f senderFunc) SendOnly(ctx context.Context, p *user.PreparedInference, w io.Writer, receipt func()) (*host.HostResponse, error) {
	return f(ctx, p, w, receipt)
}

type flushWriter struct {
	strings.Builder
	flushed bool
}

func (w *flushWriter) Flush() { w.flushed = true }

func TestSendRecoveringPreservesReplyErrorAndWriter(t *testing.T) {
	reply := &host.HostResponse{ConfirmedAt: 123}
	failure := errors.New("read failed after reply")
	writer := &flushWriter{}
	receiptSeen := false
	sender := senderFunc(func(_ context.Context, _ *user.PreparedInference, w io.Writer, receipt func()) (*host.HostResponse, error) {
		if w != writer {
			t.Fatal("writer was replaced")
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("writer lost Flush")
		}
		flusher.Flush()
		receipt()
		return reply, failure
	})
	actual, err := SendRecovering(context.Background(), sender, nil, writer, func() { receiptSeen = true })
	if actual != reply || !errors.Is(err, failure) {
		t.Fatalf("result = %p, %v; want original reply and error", actual, err)
	}
	if !writer.flushed || !receiptSeen {
		t.Fatal("flush or receipt callback was lost")
	}
}

func TestSendRecoveringPreservesPanicClassification(t *testing.T) {
	sender := senderFunc(func(context.Context, *user.PreparedInference, io.Writer, func()) (*host.HostResponse, error) {
		panic("malformed host reply")
	})
	reply, err := SendRecovering(context.Background(), sender, nil, nil, nil)
	if reply != nil || !errors.Is(err, ErrSendPanicked) || !strings.Contains(err.Error(), "malformed host reply") {
		t.Fatalf("result = %v, %v; want classified panic", reply, err)
	}
}

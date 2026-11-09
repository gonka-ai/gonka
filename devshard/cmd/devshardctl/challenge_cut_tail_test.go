package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

// cutMidEventClient relays the host's stream up to the inside of its first
// content event and then fails the attempt: the cut lands before the event's
// newline, so the content reaches the gateway unclassified.
type cutMidEventClient struct {
	inner user.HostClient
}

func (c *cutMidEventClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if req.Payload == nil {
		return c.inner.Send(ctx, req, stream, receiptHandler)
	}
	resp, err := c.inner.Send(ctx, req, &cutInsideContentWriter{w: stream}, receiptHandler)
	if err != nil {
		return resp, err
	}
	resp.ReceivedComplete = false
	return resp, errors.New("stream cut inside the content event")
}

// cutInsideContentWriter passes the role event whole, the content event
// without its newlines, and nothing after it.
type cutInsideContentWriter struct {
	w      io.Writer
	events int
}

func (c *cutInsideContentWriter) Write(p []byte) (int, error) {
	c.events++
	switch c.events {
	case 1:
		return c.w.Write(p)
	case 2:
		if _, err := c.w.Write(bytes.TrimRight(p, "\n")); err != nil {
			return 0, err
		}
		return len(p), nil
	default:
		return len(p), nil
	}
}

// A stream cut inside a content event carried content: it is challenged as a
// stream that did not bind, not as an empty one. The classifier holds the cut
// event until the stream ends, so the reason must be decided after that tail
// is classified, on the failed path as on the successful one.
func TestAStreamCutInsideItsContentEventIsNotEmpty(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	for _, client := range env.killables {
		client.inner = &cutMidEventClient{inner: client.inner}
	}
	var mu sync.Mutex
	reasons := map[uint64]types.ChallengeReason{}
	env.session.SetChallengeHandler(func(nonce uint64, reason types.ChallengeReason) {
		mu.Lock()
		defer mu.Unlock()
		reasons[nonce] = reason
	})

	var served bytes.Buffer
	_ = env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &served, nil)
	env.proxy.redundancy.waitRaceCleanups()
	// A Finish not yet applied when the race settled comes with the next
	// diff round; the challenge a mark waits for starts when its Finish applies.
	require.NoError(t, env.session.SendPendingDiff(context.Background()))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reasons) > 0
	}, 5*time.Second, 10*time.Millisecond, "the cut stream is challenged once its Finish applies")
	mu.Lock()
	defer mu.Unlock()
	for nonce, reason := range reasons {
		require.Equal(t, types.ChallengeReasonLabel(types.ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND), types.ChallengeReasonLabel(reason), "nonce %d", nonce)
	}
}

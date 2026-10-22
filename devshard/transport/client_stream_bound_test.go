package transport

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	streamBoundEscrowID = "escrow-stream-bound"
	// sseTerminator is the frame a host ends an answer with.
	sseTerminator = "data: [DONE]\n\n"
)

// sseFrames renders frames without the terminator, so a bound can sit between.
func sseFrames(frameCount, contentBytes int) string {
	var frames strings.Builder
	for i := 0; i < frameCount; i++ {
		frames.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("A", contentBytes) + "\"}}]}\n\n")
	}
	return frames.String()
}

func streamBoundClient(limit int64) *HTTPClient {
	config := DefaultClientConfig()
	config.MaxSSEStreamBytes = limit
	return &HTTPClient{escrowID: streamBoundEscrowID, config: config}
}

// The per-line cap misses a stream that stays small per line and never ends.
func TestParseSSE_StreamPastTheTotalBoundAborts(t *testing.T) {
	stream := sseFrames(8, 64) + sseTerminator
	client := streamBoundClient(int64(len(stream)) - 1)

	_, err := client.parseSSEResponse(context.Background(), strings.NewReader(stream), nil, nil)

	require.ErrorIs(t, err, ErrSSEStreamTooLarge)
}

func TestParseSSE_StreamAtTheTotalBoundCompletes(t *testing.T) {
	stream := sseFrames(8, 64) + sseTerminator
	client := streamBoundClient(int64(len(stream)))

	_, err := client.parseSSEResponse(context.Background(), strings.NewReader(stream), nil, nil)

	require.NoError(t, err, "a stream that lands exactly on the bound is within it")
}

// The crossing line arrived whole; dropping it would lose a meta tail.
func TestParseSSE_StreamDeliversTheLineThatCrossesTheBound(t *testing.T) {
	frames := sseFrames(8, 64)
	client := streamBoundClient(int64(len(frames)))
	var forwarded bytes.Buffer

	_, err := client.parseSSEResponse(context.Background(), strings.NewReader(frames+sseTerminator), &forwarded, nil)

	require.ErrorIs(t, err, ErrSSEStreamTooLarge)
	require.Contains(t, forwarded.String(), "[DONE]",
		"the frame that crossed the bound was complete and must still be handled")
}

// A committed-but-empty compressed response reads as unexpected EOF.
func TestParseSSE_UnexpectedEOFIsATruncatedStream(t *testing.T) {
	client := streamBoundClient(DefaultMaxSSEStreamBytes)

	_, err := client.parseSSEResponse(context.Background(),
		&truncatedReader{data: []byte("data: {\"choices\":[]}\n\n")}, nil, nil)

	require.ErrorIs(t, err, ErrSSEStreamTruncated)
}

// resetReader ends with a transport error that is not an EOF.
type resetReader struct{ remaining string }

func (r *resetReader) Read(p []byte) (int, error) {
	if r.remaining == "" {
		return 0, errors.New("connection reset by peer")
	}
	n := copy(p, r.remaining)
	r.remaining = r.remaining[n:]
	return n, nil
}

// Only an EOF-shaped end is a truncation.
func TestParseSSE_NonEOFErrorKeepsItsOwnName(t *testing.T) {
	client := streamBoundClient(DefaultMaxSSEStreamBytes)

	_, err := client.parseSSEResponse(context.Background(),
		&resetReader{remaining: "data: {\"choices\":[]}\n\n"}, nil, nil)

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrSSEStreamTruncated)
}

// A stream we cancelled is ours, meta tail or not.
func TestParseSSE_CancelledStreamWithMetaBlamesTheCaller(t *testing.T) {
	client := streamBoundClient(DefaultMaxSSEStreamBytes)
	body := receiptOnlySSE + "data: [DONE]\n\n" + sseMetaWithFinish(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := client.parseSSEResponse(ctx, &truncatedReader{data: []byte(body)}, nil, nil)

	require.ErrorIs(t, err, context.Canceled)
	require.True(t, userHasFinish(result.Mempool, 1), "the artifact the host did send must survive")
}

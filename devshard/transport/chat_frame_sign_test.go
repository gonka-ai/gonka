package transport

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
)

type sentFrame struct {
	chunk       []byte
	sig         []byte
	streamBytes uint64
}

func newRecordingSink(frames *[]sentFrame) *ChatFrameSink {
	return NewSignedChatFrameSink(func(chunk, sig []byte, streamBytes uint64) error {
		*frames = append(*frames, sentFrame{
			chunk:       append([]byte(nil), chunk...),
			sig:         append([]byte(nil), sig...),
			streamBytes: streamBytes,
		})
		return nil
	})
}

var testPromptHash = bytes.Repeat([]byte{7}, 32)

// executorStream runs the executor's write pattern through a signing sink:
// the receipt with FlushNow, a role delta, n content tokens, [DONE] and meta.
func executorStream(t *testing.T, schedule *host.ChatFrameSchedule, signer signing.Signer, n int) (string, []sentFrame) {
	t.Helper()
	var frames []sentFrame
	sink := newRecordingSink(&frames)
	sink.SignFrames(schedule, NewChatFrameSigner(signer, "escrow-1", 1, testPromptHash))
	var body strings.Builder
	write := func(event string) {
		body.WriteString(event)
		require.NoError(t, relaySSELines(event, sink))
	}
	write("data: {\"devshard_receipt\":{}}\n\n")
	require.NoError(t, sink.FlushNow())
	write("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
	for i := 1; i <= n; i++ {
		write(vllmTokenEvent(i, fmt.Sprintf(" tok%d", i)))
	}
	write("data: [DONE]\n\n")
	body.WriteString("data: {\"devshard_meta\":{}}\n\n")
	_, err := fmt.Fprint(sink, "data: {\"devshard_meta\":{}}\n\n")
	require.NoError(t, err)
	require.NoError(t, sink.Close())
	return body.String(), frames
}

// verifyFrames reads frames the way the gateway does: the verifier sees each
// frame's signature as it arrives and the decompressed bytes as they inflate.
func verifyFrames(t *testing.T, frames []sentFrame, allowed func(string) bool) (string, host.ChatFrameCheck) {
	t.Helper()
	v := NewChatFrameVerifier("escrow-1", 1, testPromptHash, allowed)
	r := &frameFeed{frames: frames, verifier: v}
	gz, err := gzip.NewReader(r)
	require.NoError(t, err)
	var out bytes.Buffer
	buf := make([]byte, 7) // small reads cross frame boundaries
	for {
		n, err := gz.Read(buf)
		if n > 0 {
			v.Write(buf[:n])
			out.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	v.Close()
	return out.String(), v.Check()
}

type frameFeed struct {
	frames   []sentFrame
	verifier *ChatFrameVerifier
	buf      []byte
}

func (f *frameFeed) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		if len(f.frames) == 0 {
			return 0, io.EOF
		}
		frame := f.frames[0]
		f.frames = f.frames[1:]
		f.verifier.Frame(frame.sig, frame.streamBytes)
		f.buf = frame.chunk
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func signedCount(frames []sentFrame) int {
	n := 0
	for _, f := range frames {
		if len(f.sig) > 0 {
			n++
		}
	}
	return n
}

func TestChatFrameSink_JoinedScheduleSignsEveryGroup(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	body, frames := executorStream(t, nil, signer, 12)

	// receipt, role, first token, two groups of five, then the tail with the
	// last two tokens, [DONE] and meta. The role delta leaves alone, unsigned:
	// the first token's signature covers it.
	require.Len(t, frames, 6)
	require.Equal(t, 5, signedCount(frames))
	require.Empty(t, frames[1].sig, "the role delta before the first output")
	var covered uint64
	for _, f := range frames {
		if len(f.sig) > 0 {
			require.Greater(t, f.streamBytes, covered, "each signature covers new bytes")
			covered = f.streamBytes
		}
	}
	require.Equal(t, uint64(len(body)), frames[len(frames)-1].streamBytes, "the last signature covers the whole stream")

	got, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Equal(t, body, got)
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(5), check.Signed)
	require.Equal(t, uint64(6), check.Frames)
}

func TestChatFrameSink_FirstOutputLeavesAlone(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var frames []sentFrame
	sink := newRecordingSink(&frames)
	sink.SignFrames(nil, NewChatFrameSigner(signer, "escrow-1", 1, testPromptHash))
	require.NoError(t, relaySSELines("data: {\"devshard_receipt\":{}}\n\n", sink))
	require.NoError(t, sink.FlushNow())
	require.Len(t, frames, 1, "the receipt leaves on FlushNow")
	require.NoError(t, relaySSELines("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n", sink))
	require.Len(t, frames, 2, "a role delta before any output leaves alone")
	require.NoError(t, relaySSELines(vllmTokenEvent(1, "Hi"), sink))
	require.Len(t, frames, 3, "the first token is not held for its group")
	for i := 2; i <= 5; i++ {
		require.NoError(t, relaySSELines(vllmTokenEvent(i, "x"), sink))
		require.Len(t, frames, 3, "later tokens wait for their group")
	}
	require.NoError(t, relaySSELines(vllmTokenEvent(6, "x"), sink))
	require.Len(t, frames, 4, "the fifth token after the first output closes the group")
	require.NoError(t, sink.Close())
}

func TestChatFrameSink_SeparateScheduleFlushesEachEventSignsEveryN(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	body, frames := executorStream(t, &host.ChatFrameSchedule{Every: 4, Separate: true}, signer, 12)

	// receipt, role, first token, then eleven tokens and [DONE] one frame each,
	// then meta on Close.
	require.Len(t, frames, 16)
	// Signed: receipt, first token, every fourth event after it, Close.
	require.Equal(t, 6, signedCount(frames))
	for _, f := range frames {
		if len(f.sig) == 0 {
			require.Zero(t, f.streamBytes, "an unsigned frame names no prefix")
		}
	}

	got, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Equal(t, body, got)
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(6), check.Signed)
	require.Equal(t, uint64(16), check.Frames)
}

func TestChatFrameSink_UnsignedUntilSignFrames(t *testing.T) {
	var frames []sentFrame
	sink := newRecordingSink(&frames)
	for i := 1; i <= 3; i++ {
		require.NoError(t, relaySSELines(vllmTokenEvent(i, "x"), sink))
	}
	require.Len(t, frames, 3, "with no signer the sink keeps one frame per event")
	require.Zero(t, signedCount(frames))
	require.NoError(t, sink.Close())
}

func TestChatFrameSink_SignFramesAfterAWriteIsIgnored(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var frames []sentFrame
	sink := newRecordingSink(&frames)
	require.NoError(t, relaySSELines("data: {\"devshard_receipt\":{}}\n\n", sink))
	sink.SignFrames(nil, NewChatFrameSigner(signer, "escrow-1", 1, testPromptHash))
	require.NoError(t, relaySSELines(vllmTokenEvent(1, "Hi"), sink))
	require.NoError(t, sink.Close())
	require.Zero(t, signedCount(frames), "a signature must cover the stream from its first byte")
}

func TestChatFrameSink_CloseDoesNotSignTheSamePrefixTwice(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	var frames []sentFrame
	sink := newRecordingSink(&frames)
	sink.SignFrames(&host.ChatFrameSchedule{Every: 1}, NewChatFrameSigner(signer, "escrow-1", 1, testPromptHash))
	require.NoError(t, relaySSELines(vllmTokenEvent(1, "Hi"), sink))
	require.NoError(t, relaySSELines("data: [DONE]\n\n", sink))
	require.NoError(t, sink.Close())
	require.Len(t, frames, 3, "first token, [DONE], then the gzip trailer")
	require.NotEmpty(t, frames[1].sig)
	require.Empty(t, frames[2].sig, "the trailer adds no bytes, so it is not signed again")

	_, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(2), check.Signed)
}

func TestChatFrameVerifier_RejectsTheSamePrefixSignedTwice(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, signer, 6)
	dup := frames[len(frames)-1]
	frames = append(frames, sentFrame{sig: dup.sig, streamBytes: dup.streamBytes})
	_, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Contains(t, check.Invalid, "behind the stream")
}

func TestChatFrameVerifier_RejectsAnotherSigner(t *testing.T) {
	executor := testutil.MustGenerateKey(t)
	impostor := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, impostor, 6)
	_, check := verifyFrames(t, frames, func(addr string) bool { return addr == executor.Address() })
	require.Contains(t, check.Invalid, "not the executor")
	require.Zero(t, check.Signed)
}

func TestChatFrameVerifier_AcceptsAWarmKey(t *testing.T) {
	cold := testutil.MustGenerateKey(t)
	warm := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, warm, 6)
	allowed := func(addr string) bool { return addr == cold.Address() || addr == warm.Address() }
	_, check := verifyFrames(t, frames, allowed)
	require.Empty(t, check.Invalid)
	require.NotZero(t, check.Signed)
}

func TestChatFrameVerifier_DetectsChangedBytes(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	body, frames := executorStream(t, nil, signer, 12)

	// The same signatures over a stream that differs in one token.
	tampered := strings.Replace(body, "tok7", "tok8", 1)
	require.NotEqual(t, body, tampered)
	v := NewChatFrameVerifier("escrow-1", 1, testPromptHash, func(addr string) bool { return addr == signer.Address() })
	for _, f := range frames {
		if len(f.sig) > 0 {
			v.Frame(f.sig, f.streamBytes)
		}
	}
	v.Write([]byte(tampered))
	v.Close()
	check := v.Check()
	require.Contains(t, check.Invalid, "does not match the stream")
	require.NotZero(t, check.Signed, "signatures over the untouched prefix still verify")
}

func TestChatFrameVerifier_ReportsASignatureBeyondTheStream(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, signer, 6)
	last := &frames[len(frames)-1]
	last.streamBytes += 10
	_, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Contains(t, check.Invalid, "before the")
}

func TestChatFrameVerifier_BindsThePrompt(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, signer, 3)
	v := NewChatFrameVerifier("escrow-1", 1, bytes.Repeat([]byte{8}, 32), func(addr string) bool { return addr == signer.Address() })
	r := &frameFeed{frames: frames, verifier: v}
	gz, err := gzip.NewReader(r)
	require.NoError(t, err)
	data, err := io.ReadAll(gz)
	require.NoError(t, err)
	v.Write(data)
	v.Close()
	require.NotEmpty(t, v.Check().Invalid, "a signature for one prompt does not verify under another")
}

func TestSSEEventCarriesOutput(t *testing.T) {
	cases := map[string]bool{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n": false,
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n":                      true,
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hm\"}}]}\n\n":            true,
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\n\n":          true,
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[]}}]}\n\n":                       false,
		"data: {\"error\":{\"message\":\"boom\"}}\n\n":                                    true,
		"data: [DONE]\n\n":                    true,
		"data: {\"devshard_receipt\":{}}\n\n": false,
		"data: not json\n\n":                  true,
		": keep-alive\n\n":                    false,
	}
	for event, want := range cases {
		require.Equal(t, want, sseEventCarriesOutput([]byte(event)), event)
	}
}

// A compact signature recovers to two keys. Asking about the wrong one would
// send the gateway to the chain about an address nobody holds, and the
// gateway caches every answer.
func TestChatFrameVerifier_AsksOnlyAboutTheSigner(t *testing.T) {
	for i := 0; i < 20; i++ {
		signer := testutil.MustGenerateKey(t)
		_, frames := executorStream(t, nil, signer, 6)
		var asked []string
		_, check := verifyFrames(t, frames, func(addr string) bool {
			asked = append(asked, addr)
			return addr == signer.Address()
		})
		require.Empty(t, check.Invalid)
		require.Equal(t, []string{signer.Address()}, asked, "one question, about the key that signed")
	}
}

func TestChatFrameVerifier_DoesNotAskWhileTheStreamIsRead(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, signer, 6)
	asked := 0
	v := NewChatFrameVerifier("escrow-1", 1, testPromptHash, func(string) bool { asked++; return true })
	r := &frameFeed{frames: frames, verifier: v}
	gz, err := gzip.NewReader(r)
	require.NoError(t, err)
	data, err := io.ReadAll(gz)
	require.NoError(t, err)
	v.Write(data)
	require.Zero(t, asked, "no authorization while tokens flow")
	v.Close()
	require.Equal(t, 1, asked)
	require.NotZero(t, v.Check().Signed)
}

// A host that predates signed frames sends none. That is not a failure, and
// the gateway asks no one about it.
func TestChatFrameVerifier_UnsignedStreamIsNotAFailure(t *testing.T) {
	var frames []sentFrame
	sink := newRecordingSink(&frames)
	require.NoError(t, relaySSELines("data: {\"devshard_receipt\":{}}\n\n", sink))
	require.NoError(t, relaySSELines(vllmTokenEvent(1, "Hi"), sink))
	require.NoError(t, relaySSELines("data: [DONE]\n\n", sink))
	require.NoError(t, sink.Close())
	_, check := verifyFrames(t, frames, func(string) bool {
		t.Fatal("an unsigned stream has no signer to ask about")
		return false
	})
	require.Empty(t, check.Invalid)
	require.Zero(t, check.Signed)
	require.Equal(t, uint64(len(frames)), check.Frames)
}

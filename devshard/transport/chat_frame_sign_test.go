package transport

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

type sentFrame struct {
	chunk       []byte
	sig         []byte
	streamBytes uint64
	tokenCount  uint64
}

func newRecordingSink(frames *[]sentFrame) *ChatFrameSink {
	return NewSignedChatFrameSink(func(chunk, sig []byte, streamBytes, tokenCount uint64) error {
		*frames = append(*frames, sentFrame{
			chunk:       append([]byte(nil), chunk...),
			sig:         append([]byte(nil), sig...),
			streamBytes: streamBytes,
			tokenCount:  tokenCount,
		})
		return nil
	})
}

var testPromptHash = bytes.Repeat([]byte{7}, 32)

// executorStream runs the executor's write pattern through a signing sink:
// the receipt with FlushNow, n content tokens, [DONE] and meta. A role-only
// delta is not part of the pattern; the sink drops it.
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
		f.verifier.Frame(frame.sig, frame.streamBytes, frame.tokenCount)
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

func TestChatFramePreimage_OmitsSeqAndStreamBytes(t *testing.T) {
	contentHash := bytes.Repeat([]byte{9}, 32)
	preimage, err := chatFramePreimage("escrow-1", 7, contentHash, 11)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(preimage, []byte(types.DomainChatFrame)))
	body := preimage[len(types.DomainChatFrame):]

	msg := &types.ChatFrameSignatureContent{}
	require.NoError(t, proto.Unmarshal(body, msg))
	require.Equal(t, "escrow-1", msg.GetEscrowId())
	require.Equal(t, uint64(7), msg.GetInferenceId())
	require.Equal(t, contentHash, msg.GetContentHash())
	require.Equal(t, uint64(11), msg.GetTokenCount())

	fields := msg.ProtoReflect().Descriptor().Fields()
	names := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		names = append(names, string(fields.Get(i).Name()))
	}
	require.Equal(t, []string{"escrow_id", "inference_id", "content_hash", "token_count"}, names)

	var nums []protowire.Number
	rest := body
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		require.Positive(t, n)
		rest = rest[n:]
		vn := protowire.ConsumeFieldValue(num, typ, rest)
		require.Positive(t, vn)
		rest = rest[vn:]
		nums = append(nums, num)
	}
	require.Equal(t, []protowire.Number{1, 2, 3, 4}, nums)

	other, err := chatFramePreimage("escrow-1", 7, contentHash, 12)
	require.NoError(t, err)
	require.NotEqual(t, preimage, other, "token_count is inside the preimage")
}

func TestChatFrameSink_DefaultJoinAndSignConstants(t *testing.T) {
	require.Equal(t, 5, DefaultChatFrameJoin)
	require.Equal(t, 13, DefaultChatFrameSignEvery)
	require.Equal(t, DefaultChatFrameJoin, chatFrameJoin(nil))
	require.Equal(t, DefaultChatFrameSignEvery, chatFrameSignEvery(nil))
}

type chunkWant struct {
	tokens []string
	done   bool
	signed bool
	count  uint64
	// absent is the content token that opened the next chunk. A signature
	// over this chunk does not cover it.
	absent string
}

func TestChatFrameSink_JoinEveryXSignEveryN(t *testing.T) {
	cases := []struct {
		name        string
		join, every uint32
		tokens      int
		want        []chunkWant
	}{
		{
			name:   "defaults, short of a checkpoint",
			join:   5,
			every:  13,
			tokens: 10,
			want: []chunkWant{
				{tokens: []string{"t1"}},
				{tokens: []string{"t2", "t3", "t4", "t5", "t6"}},
				{tokens: []string{"t7", "t8", "t9", "t10"}, done: true, signed: true, count: 10},
			},
		},
		{
			name:   "signature on an intermediate chunk",
			join:   2,
			every:  3,
			tokens: 7,
			want: []chunkWant{
				{tokens: []string{"t1"}},
				{tokens: []string{"t2", "t3"}},
				{tokens: []string{"t4", "t5"}, signed: true, count: 5, absent: "t6"},
				{tokens: []string{"t6", "t7"}, done: true, signed: true, count: 7},
			},
		},
		{
			name:   "every chunk signed",
			join:   2,
			every:  1,
			tokens: 4,
			want: []chunkWant{
				{tokens: []string{"t1"}, signed: true, count: 1, absent: "t2"},
				{tokens: []string{"t2", "t3"}, signed: true, count: 3, absent: "t4"},
				{tokens: []string{"t4"}, done: true, signed: true, count: 4},
			},
		},
		{
			name:   "one token",
			join:   5,
			every:  13,
			tokens: 1,
			want: []chunkWant{
				{tokens: []string{"t1"}, done: true, signed: true, count: 1},
			},
		},
		{
			name:   "empty",
			join:   5,
			every:  13,
			tokens: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, frames := driveTokens(t, &host.ChatFrameSchedule{Join: tc.join, SignEvery: tc.every}, tc.tokens, true)
			chunks := contentChunks(t, frames)
			require.Len(t, chunks, len(tc.want))
			var signedN int
			for i, want := range tc.want {
				got := chunks[i]
				if want.signed {
					signedN++
					require.NotEmpty(t, got.frame.sig)
					require.Equal(t, want.count, got.frame.tokens, "token_count")
					prefix := inflateSent(t, sentOnly(frames)[:got.end+1])
					require.Equal(t, uint64(len(prefix)), got.frame.streamBytes)
					requireContentHash(t, key, got.frame.tokens, prefix, got.frame.sig)
					if want.absent != "" {
						require.NotContains(t, prefix, contentMarker(want.absent), "the token that opened the next chunk is not in the signed prefix")
						require.Contains(t, inflateSent(t, sentOnly(frames)), contentMarker(want.absent))
					}
				} else {
					require.Empty(t, got.frame.sig)
					require.Zero(t, got.frame.streamBytes)
				}
				for _, tok := range want.tokens {
					require.Contains(t, got.text, contentMarker(tok))
				}
				if want.done {
					require.Contains(t, got.text, "data: [DONE]")
				} else {
					require.NotContains(t, got.text, "[DONE]")
				}
			}
			full := inflateSent(t, sentOnly(frames))
			prev := 0
			for i := 1; i <= tc.tokens; i++ {
				at := strings.Index(full, contentMarker(fmt.Sprintf("t%d", i)))
				require.GreaterOrEqual(t, at, prev, "token t%d", i)
				prev = at
			}
			if tc.tokens == 0 {
				require.NotContains(t, full, "[DONE]")
				require.Zero(t, signedCount(sentOnly(frames)))
				require.Empty(t, frames)
				return
			}
			got, check := verifyFrames(t, sentOnly(frames), func(addr string) bool { return addr == key.Address() })
			require.Equal(t, full, got)
			require.Empty(t, check.Invalid)
			require.Equal(t, uint64(signedN), check.Signed)
		})
	}
}

func TestChatFrameSink_SignEveryCoversUnsignedJoins(t *testing.T) {
	key, frames := driveTokens(t, &host.ChatFrameSchedule{Join: 2, SignEvery: 3}, 5, true)
	chunks := contentChunks(t, frames)
	require.Len(t, chunks, 3)
	require.Empty(t, chunks[0].frame.sig)
	require.Empty(t, chunks[1].frame.sig)
	require.NotEmpty(t, chunks[2].frame.sig)
	require.Equal(t, uint64(5), chunks[2].frame.tokens)

	prefix := inflateSent(t, sentOnly(frames)[:chunks[2].end+1])
	require.Contains(t, prefix, contentMarker("t1"))
	require.Contains(t, prefix, contentMarker("t2"))
	require.Contains(t, prefix, contentMarker("t3"))
	require.Contains(t, prefix, contentMarker("t4"))
	require.Contains(t, prefix, contentMarker("t5"))
	require.Contains(t, prefix, "data: [DONE]")
	requireContentHash(t, key, chunks[2].frame.tokens, prefix, chunks[2].frame.sig)
	require.Equal(t, uint64(len(prefix)), chunks[2].frame.streamBytes)

	_, check := verifyFrames(t, sentOnly(frames), func(addr string) bool { return addr == key.Address() })
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(1), check.Signed)
}

func TestChatFrameSink_FirstTokenWaitsForTheSecond(t *testing.T) {
	key := testutil.MustGenerateKey(t)
	var frames []scheduleFrame
	sink := newScheduleSink(&host.ChatFrameSchedule{Join: 5, SignEvery: 13}, key, &frames)
	require.NoError(t, relaySSELines(contentEvent("t1"), sink))
	require.Empty(t, frames, "the first content token stays unsent")
	require.NoError(t, relaySSELines(contentEvent("t2"), sink))
	chunks := contentChunks(t, frames)
	require.Len(t, chunks, 1)
	require.Contains(t, chunks[0].text, contentMarker("t1"))
	require.NotContains(t, chunks[0].text, contentMarker("t2"))
	require.NoError(t, sink.Close())
}

func TestChatFrameSink_DoneJoinsTheOpenMessage(t *testing.T) {
	_, frames := driveTokens(t, &host.ChatFrameSchedule{Join: 5, SignEvery: 13}, 6, true)
	chunks := contentChunks(t, frames)
	require.Len(t, chunks, 2)
	var doneFrames int
	for _, c := range chunks {
		if !strings.Contains(c.text, "[DONE]") {
			continue
		}
		doneFrames++
		require.Contains(t, c.text, contentMarker("t2"))
		require.Contains(t, c.text, contentMarker("t6"))
		require.NotEqual(t, "data: [DONE]\n\n", c.text, "no frame is only [DONE]")
		require.NotEmpty(t, c.frame.sig)
	}
	require.Equal(t, 1, doneFrames)
}

func TestChatFrameSink_DoneWithoutContentSendsNothing(t *testing.T) {
	_, frames := driveTokens(t, &host.ChatFrameSchedule{Join: 5, SignEvery: 13}, 0, true)
	require.Empty(t, contentChunks(t, frames))
	require.Zero(t, signedCount(sentOnly(frames)))
}

// One flush may carry the answer and [DONE] together. Each finished event is
// scheduled on its own, so [DONE] joins the answer instead of discarding it.
func TestChatFrameSink_OneFlushWithDoneKeepsTheAnswer(t *testing.T) {
	t.Run("non-streaming body", func(t *testing.T) {
		key := testutil.MustGenerateKey(t)
		var frames []scheduleFrame
		sink := newScheduleSink(&host.ChatFrameSchedule{Join: 5, SignEvery: 13}, key, &frames)
		body := "data: {\"choices\":[{\"message\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n"
		_, err := sink.Write([]byte(body))
		require.NoError(t, err)
		require.NoError(t, sink.FlushErr())
		require.NoError(t, sink.Close())

		chunks := contentChunks(t, frames)
		require.Len(t, chunks, 1)
		require.Contains(t, chunks[0].text, contentMarker("Hi"))
		require.Contains(t, chunks[0].text, "data: [DONE]")
		require.NotEqual(t, "data: [DONE]\n\n", chunks[0].text)
		require.NotEmpty(t, chunks[0].frame.sig)
		require.Equal(t, uint64(1), chunks[0].frame.tokens)
		prefix := inflateSent(t, sentOnly(frames)[:chunks[0].end+1])
		requireContentHash(t, key, 1, prefix, chunks[0].frame.sig)

		_, check := verifyFrames(t, sentOnly(frames), func(addr string) bool { return addr == key.Address() })
		require.Empty(t, check.Invalid)
		require.Equal(t, uint64(1), check.Signed)
		require.Equal(t, []uint64{1}, check.TokenCounts)
		require.False(t, check.TokenCountMismatch)
	})

	t.Run("replay matches per-event flushes", func(t *testing.T) {
		schedule := &host.ChatFrameSchedule{Join: 5, SignEvery: 13}
		_, perEvent := driveTokens(t, schedule, 6, true)
		key := testutil.MustGenerateKey(t)
		var frames []scheduleFrame
		sink := newScheduleSink(schedule, key, &frames)
		var body strings.Builder
		for i := 1; i <= 6; i++ {
			body.WriteString(contentEvent(fmt.Sprintf("t%d", i)))
		}
		body.WriteString("data: [DONE]\n\n")
		_, err := sink.Write([]byte(body.String()))
		require.NoError(t, err)
		require.NoError(t, sink.FlushErr())
		require.NoError(t, sink.Close())

		want := contentChunks(t, perEvent)
		got := contentChunks(t, frames)
		require.Len(t, got, len(want))
		for i := range want {
			require.Equal(t, want[i].text, got[i].text, "chunk %d", i)
			require.Equal(t, len(want[i].frame.sig) > 0, len(got[i].frame.sig) > 0, "chunk %d signed", i)
			require.Equal(t, want[i].frame.tokens, got[i].frame.tokens, "chunk %d token_count", i)
		}
		_, check := verifyFrames(t, sentOnly(frames), func(addr string) bool { return addr == key.Address() })
		require.Empty(t, check.Invalid)
		require.False(t, check.TokenCountMismatch)
		require.Equal(t, uint64(6), check.ContentTokens)
	})

	t.Run("done alone", func(t *testing.T) {
		key := testutil.MustGenerateKey(t)
		var frames []scheduleFrame
		sink := newScheduleSink(&host.ChatFrameSchedule{Join: 5, SignEvery: 13}, key, &frames)
		_, err := sink.Write([]byte("data: [DONE]\n\n"))
		require.NoError(t, err)
		require.NoError(t, sink.FlushErr())
		require.NoError(t, sink.Close())
		require.Empty(t, frames)
	})
}

func TestChatFrameSink_EmptyContentSendsNothing(t *testing.T) {
	empty := []string{
		`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":""}}]}` + "\n\n",
		`data: {"choices":[{"message":{"role":"assistant","content":""}}]}` + "\n\n",
		`data: {"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":1}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	schedule := &host.ChatFrameSchedule{Join: 5, SignEvery: 13}

	t.Run("each event flushed", func(t *testing.T) {
		key := testutil.MustGenerateKey(t)
		var frames []scheduleFrame
		sink := newScheduleSink(schedule, key, &frames)
		for _, ev := range empty {
			require.NoError(t, relaySSELines(ev, sink))
		}
		require.NoError(t, sink.Close())
		require.Empty(t, frames)
	})

	t.Run("one flush", func(t *testing.T) {
		key := testutil.MustGenerateKey(t)
		var frames []scheduleFrame
		sink := newScheduleSink(schedule, key, &frames)
		_, err := sink.Write([]byte(strings.Join(empty, "")))
		require.NoError(t, err)
		require.NoError(t, sink.FlushErr())
		require.NoError(t, sink.Close())
		require.Empty(t, frames)
	})
}

func TestChatFrameSink_EmptyEventsDoNotAdvanceJoinOrSign(t *testing.T) {
	schedule := &host.ChatFrameSchedule{Join: 2, SignEvery: 3}
	_, plain := driveTokens(t, schedule, 5, true)
	noisyKey := testutil.MustGenerateKey(t)
	var noisy []scheduleFrame
	sink := newScheduleSink(schedule, noisyKey, &noisy)
	noise := []string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n",
	}
	for _, ev := range noise {
		require.NoError(t, relaySSELines(ev, sink))
	}
	for i := 1; i <= 5; i++ {
		for _, ev := range noise {
			require.NoError(t, relaySSELines(ev, sink))
		}
		require.NoError(t, relaySSELines(contentEvent(fmt.Sprintf("t%d", i)), sink))
		for _, ev := range noise {
			require.NoError(t, relaySSELines(ev, sink))
		}
	}
	require.NoError(t, relaySSELines("data: [DONE]\n\n", sink))
	require.NoError(t, sink.Close())

	plainChunks := contentChunks(t, plain)
	noisyChunks := contentChunks(t, noisy)
	require.Len(t, noisyChunks, len(plainChunks))
	for i := range plainChunks {
		require.Equal(t, plainChunks[i].text, noisyChunks[i].text)
		require.Equal(t, len(plainChunks[i].frame.sig) > 0, len(noisyChunks[i].frame.sig) > 0)
		require.Equal(t, plainChunks[i].frame.tokens, noisyChunks[i].frame.tokens)
	}
	require.Equal(t, uint64(5), noisyChunks[len(noisyChunks)-1].frame.tokens)
}

func TestChatFrameSink_ErrorSignsTheOpenMessage(t *testing.T) {
	key := testutil.MustGenerateKey(t)
	var frames []scheduleFrame
	sink := newScheduleSink(&host.ChatFrameSchedule{Join: 5, SignEvery: 13}, key, &frames)
	require.NoError(t, relaySSELines(contentEvent("t1"), sink))
	require.NoError(t, relaySSELines(contentEvent("t2"), sink))
	require.NoError(t, relaySSELines("data: {\"error\":{\"message\":\"boom\"}}\n\n", sink))
	require.NoError(t, sink.Close())

	chunks := contentChunks(t, frames)
	require.Len(t, chunks, 2, "no frame follows the error")
	require.Empty(t, chunks[0].frame.sig)
	require.Contains(t, chunks[0].text, contentMarker("t1"))
	require.NotEmpty(t, chunks[1].frame.sig)
	require.Contains(t, chunks[1].text, contentMarker("t2"))
	require.Contains(t, chunks[1].text, "boom")
	require.Equal(t, uint64(2), chunks[1].frame.tokens)
	prefix := inflateSent(t, sentOnly(frames)[:chunks[1].end+1])
	require.Contains(t, prefix, contentMarker("t1"))
	require.Contains(t, prefix, contentMarker("t2"))
	require.Contains(t, prefix, "boom")
	requireContentHash(t, key, chunks[1].frame.tokens, prefix, chunks[1].frame.sig)

	_, check := verifyFrames(t, sentOnly(frames), func(addr string) bool { return addr == key.Address() })
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(1), check.Signed)
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
	sink.SignFrames(&host.ChatFrameSchedule{Join: 1}, NewChatFrameSigner(signer, "escrow-1", 1, testPromptHash))
	require.NoError(t, relaySSELines(vllmTokenEvent(1, "Hi"), sink))
	require.NoError(t, relaySSELines("data: [DONE]\n\n", sink))
	require.NoError(t, sink.Close())
	require.Equal(t, 1, signedCount(frames), "the tail signature covers the token and [DONE]")
	require.Empty(t, frames[len(frames)-1].sig, "the trailer adds no bytes, so it is not signed again")
	var signedAt uint64
	for _, f := range frames {
		if len(f.sig) > 0 {
			require.Zero(t, signedAt, "the prefix is signed once")
			signedAt = f.streamBytes
		}
	}

	_, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(1), check.Signed)
}

func TestChatFrameEvidence_VerifiesWithoutTheChunk(t *testing.T) {
	key := testutil.MustGenerateKey(t)
	signer := NewChatFrameSigner(key, "escrow-1", 1, testPromptHash)
	var frames []scheduleFrame
	sink := NewSignedChatFrameSink(func(chunk, sig []byte, n, tokenCount uint64) error {
		frames = append(frames, scheduleFrame{
			sentFrame: sentFrame{
				chunk:       append([]byte(nil), chunk...),
				sig:         append([]byte(nil), sig...),
				streamBytes: n,
				tokenCount:  tokenCount,
			},
			tokens: tokenCount,
		})
		return nil
	})
	sink.SignFrames(&host.ChatFrameSchedule{Join: 5, SignEvery: 13}, signer)

	var plain strings.Builder
	write := func(event string) {
		plain.WriteString(event)
		require.NoError(t, relaySSELines(event, sink))
	}
	for i := 1; i <= 4; i++ {
		write(contentEvent(fmt.Sprintf("t%d", i)))
	}
	write("data: [DONE]\n\n")
	require.NoError(t, sink.Close())

	var signed *scheduleFrame
	for i := range frames {
		if len(frames[i].sig) == 0 {
			continue
		}
		require.Nil(t, signed, "the tail is the only signature")
		signed = &frames[i]
	}
	require.NotNil(t, signed)
	require.Equal(t, uint64(4), signed.tokenCount)
	signed.chunk = []byte("not the gzip chunk")

	h := newChatStreamHash("escrow-1", 1, testPromptHash)
	_, err := h.Write([]byte(plain.String()))
	require.NoError(t, err)

	evidence := NewChatFrameEvidence("escrow-1", 1, h.Sum(nil), signed.tokenCount, signed.sig)
	got, err := evidence.Preimage()
	require.NoError(t, err)
	require.Equal(t, signer.signedPreimage, got)
	require.True(t, evidence.Verify(key.PublicKeyBytes()))

	other := evidence
	other.InferenceID++
	otherPreimage, err := other.Preimage()
	require.NoError(t, err)
	require.NotEqual(t, got, otherPreimage)
	require.False(t, other.Verify(key.PublicKeyBytes()))
}

func TestChatFrameVerifier_TokenCountIsCovered(t *testing.T) {
	key, frames := driveTokens(t, &host.ChatFrameSchedule{Join: 5, SignEvery: 13}, 4, true)
	sent := sentOnly(frames)
	_, check := verifyFrames(t, sent, func(addr string) bool { return addr == key.Address() })
	require.Empty(t, check.Invalid)
	require.Equal(t, []uint64{4}, check.TokenCounts)

	bumped := append([]sentFrame(nil), sent...)
	found := false
	for i := range bumped {
		if len(bumped[i].sig) == 0 {
			continue
		}
		bumped[i].tokenCount++
		found = true
		break
	}
	require.True(t, found)
	_, check = verifyFrames(t, bumped, func(addr string) bool { return addr == key.Address() })
	require.NotEmpty(t, check.Invalid)
}

func TestChatFrameVerifier_DetectsAWrongTokenCount(t *testing.T) {
	key := testutil.MustGenerateKey(t)
	signer := NewChatFrameSigner(key, "escrow-1", 1, testPromptHash)
	body := contentEvent("t1") + contentEvent("t2")
	signer.add([]byte(body))
	sig, n, err := signer.sign(9)
	require.NoError(t, err)

	v := NewChatFrameVerifier("escrow-1", 1, testPromptHash, func(addr string) bool { return addr == key.Address() })
	v.Frame(sig, n, 9)
	v.Write([]byte(body))
	v.Close()
	check := v.Check()
	require.Empty(t, check.Invalid)
	require.Equal(t, uint64(1), check.Signed)
	require.True(t, check.TokenCountMismatch)
	require.Equal(t, uint64(9), check.TokenCount)
	require.Equal(t, uint64(2), check.ContentTokens)
}

func TestChatFrameVerifier_TokenCountMatchesThePrefix(t *testing.T) {
	key, frames := driveTokens(t, &host.ChatFrameSchedule{Join: 5, SignEvery: 13}, 4, true)
	_, check := verifyFrames(t, sentOnly(frames), func(addr string) bool { return addr == key.Address() })
	require.Empty(t, check.Invalid)
	require.False(t, check.TokenCountMismatch)
	require.Equal(t, []uint64{4}, check.TokenCounts)
	require.Equal(t, uint64(4), check.TokenCount)
	require.Equal(t, uint64(4), check.ContentTokens)
}

func TestChatFrameVerifier_RejectsTheSamePrefixSignedTwice(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	_, frames := executorStream(t, nil, signer, 6)
	dup := frames[len(frames)-1]
	frames = append(frames, sentFrame{sig: dup.sig, streamBytes: dup.streamBytes})
	_, check := verifyFrames(t, frames, func(addr string) bool { return addr == signer.Address() })
	require.Contains(t, check.Invalid, "behind the stream")
}

func TestChatFrameVerifier_RejectsAWrongSizeSignatureBeforeQueueingIt(t *testing.T) {
	for _, size := range []int{signing.CompactSignatureSize - 1, signing.CompactSignatureSize + 1, 1 << 20} {
		v := NewChatFrameVerifier("escrow-1", 1, testPromptHash, nil)
		// Far ahead of the stream, so a queued signature would wait.
		v.Frame(make([]byte, size), 1<<30, 1)
		require.Contains(t, v.Check().Invalid, "signature is", "size %d", size)
		require.Empty(t, v.pending, "size %d", size)
	}
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
	require.False(t, check.TokenCountMismatch)
	require.Equal(t, uint64(6), check.ContentTokens)
}

func TestChatFrameVerifier_DetectsChangedBytes(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	body, frames := executorStream(t, &host.ChatFrameSchedule{SignEvery: 1}, signer, 12)

	// The same signatures over a stream that differs in one token.
	tampered := strings.Replace(body, "tok7", "tok8", 1)
	require.NotEqual(t, body, tampered)
	v := NewChatFrameVerifier("escrow-1", 1, testPromptHash, func(addr string) bool { return addr == signer.Address() })
	for _, f := range frames {
		if len(f.sig) > 0 {
			v.Frame(f.sig, f.streamBytes, f.tokenCount)
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
	require.True(t, sseEventContentToken([]byte("data: not json\n\n")))
	require.False(t, sseEventTerminal([]byte("data: not json\n\n")))
	require.True(t, sseEventTerminal([]byte("data: [DONE]\n\n")))
	require.False(t, sseEventContentToken([]byte("data: [DONE]\n\n")))
	require.True(t, sseEventTerminal([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\n")))
	require.False(t, sseEventContentToken([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")))
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

type scheduleFrame struct {
	sentFrame
	tokens uint64
}

type textChunk struct {
	text  string
	frame scheduleFrame
	end   int
}

func newScheduleSink(schedule *host.ChatFrameSchedule, key signing.Signer, frames *[]scheduleFrame) *ChatFrameSink {
	cs := NewChatFrameSigner(key, "escrow-1", 1, testPromptHash)
	sink := NewSignedChatFrameSink(func(chunk, sig []byte, n, tokenCount uint64) error {
		*frames = append(*frames, scheduleFrame{
			sentFrame: sentFrame{
				chunk:       append([]byte(nil), chunk...),
				sig:         append([]byte(nil), sig...),
				streamBytes: n,
				tokenCount:  tokenCount,
			},
			tokens: tokenCount,
		})
		return nil
	})
	sink.SignFrames(schedule, cs)
	return sink
}

func contentEvent(token string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", token)
}

func contentMarker(token string) string {
	return fmt.Sprintf("\"content\":%q", token)
}

func driveTokens(t *testing.T, schedule *host.ChatFrameSchedule, n int, done bool) (*signing.Secp256k1Signer, []scheduleFrame) {
	t.Helper()
	key := testutil.MustGenerateKey(t)
	var frames []scheduleFrame
	sink := newScheduleSink(schedule, key, &frames)
	for i := 1; i <= n; i++ {
		require.NoError(t, relaySSELines(contentEvent(fmt.Sprintf("t%d", i)), sink))
	}
	if done {
		require.NoError(t, relaySSELines("data: [DONE]\n\n", sink))
	}
	require.NoError(t, sink.Close())
	return key, frames
}

func sentOnly(frames []scheduleFrame) []sentFrame {
	out := make([]sentFrame, len(frames))
	for i, f := range frames {
		out[i] = f.sentFrame
	}
	return out
}

func inflateSent(t *testing.T, frames []sentFrame) string {
	t.Helper()
	if len(frames) == 0 {
		return ""
	}
	var raw bytes.Buffer
	for _, f := range frames {
		raw.Write(f.chunk)
	}
	zr, err := gzip.NewReader(&raw)
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	return string(out)
}

func contentChunks(t *testing.T, frames []scheduleFrame) []textChunk {
	t.Helper()
	var out []textChunk
	prev := ""
	sent := sentOnly(frames)
	for i := range frames {
		cur := inflateSent(t, sent[:i+1])
		if cur == prev {
			continue
		}
		require.True(t, strings.HasPrefix(cur, prev), "chunk %d", i)
		out = append(out, textChunk{text: cur[len(prev):], frame: frames[i], end: i})
		prev = cur
	}
	return out
}

func requireContentHash(t *testing.T, key *signing.Secp256k1Signer, tokenCount uint64, prefix string, sig []byte) {
	t.Helper()
	h := newChatStreamHash("escrow-1", 1, testPromptHash)
	_, err := h.Write([]byte(prefix))
	require.NoError(t, err)
	preimage, err := chatFramePreimage("escrow-1", 1, h.Sum(nil), tokenCount)
	require.NoError(t, err)
	require.True(t, signing.VerifyCompact(key.PublicKeyBytes(), preimage, sig), "the signature is over the running hash and token count of this prefix")
}

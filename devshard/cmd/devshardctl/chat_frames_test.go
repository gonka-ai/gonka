package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/types"
)

func TestConfigureChatFramesFromEnv(t *testing.T) {
	t.Cleanup(func() { chatFramesEnabledDefault = true })

	t.Setenv("GATEWAY_CHAT_FRAMES", "")
	configureChatFramesFromEnv()
	require.True(t, chatFramesEnabledDefault, "unset GATEWAY_CHAT_FRAMES enables signing")

	t.Setenv("GATEWAY_CHAT_FRAMES", "0")
	configureChatFramesFromEnv()
	require.False(t, chatFramesEnabledDefault, "GATEWAY_CHAT_FRAMES=0 disables signing")

	t.Setenv("GATEWAY_CHAT_FRAMES", "false")
	configureChatFramesFromEnv()
	require.False(t, chatFramesEnabledDefault)

	t.Setenv("GATEWAY_CHAT_FRAMES", "off")
	configureChatFramesFromEnv()
	require.False(t, chatFramesEnabledDefault)

	t.Setenv("GATEWAY_CHAT_FRAMES", "1")
	configureChatFramesFromEnv()
	require.True(t, chatFramesEnabledDefault)

	t.Setenv("GATEWAY_CHAT_FRAMES", "maybe")
	configureChatFramesFromEnv()
	require.False(t, chatFramesEnabledDefault, "an unrecognized value disables signing")
}

func TestChatFramesEnabledForRequest_PerInference(t *testing.T) {
	t.Cleanup(func() { chatFramesEnabledDefault = true })
	cfg := types.SessionConfig{ChatFrameJoin: 7, ChatFrameSignEvery: 11}

	chatFramesEnabledDefault = true
	onReq := &http.Request{Header: http.Header{}}
	offReq := &http.Request{Header: http.Header{chatFramesEnabledHeader: []string{"0"}}}
	require.Equal(t, &host.ChatFrameSchedule{Join: 7, SignEvery: 11},
		chatFrameScheduleForInference(chatFramesEnabledForRequest(onReq), cfg))
	require.Nil(t, chatFrameScheduleForInference(chatFramesEnabledForRequest(offReq), cfg),
		"one request can disable signing while the process default stays on")

	chatFramesEnabledDefault = false
	require.Nil(t, chatFrameScheduleForInference(chatFramesEnabledForRequest(onReq), cfg))
	forceOn := &http.Request{Header: http.Header{chatFramesEnabledHeader: []string{"1"}}}
	require.Equal(t, &host.ChatFrameSchedule{Join: 7, SignEvery: 11},
		chatFrameScheduleForInference(chatFramesEnabledForRequest(forceOn), cfg),
		"one request can enable signing while the process default is off")

	bad := &http.Request{Header: http.Header{chatFramesEnabledHeader: []string{"maybe"}}}
	require.False(t, chatFramesEnabledForRequest(bad))
}

func TestChatFrameScheduleForInference(t *testing.T) {
	cfg := types.SessionConfig{ChatFrameJoin: 7, ChatFrameSignEvery: 11}

	require.Nil(t, chatFrameScheduleForInference(false, cfg))
	require.Equal(t, &host.ChatFrameSchedule{Join: 7, SignEvery: 11},
		chatFrameScheduleForInference(true, cfg))

	require.Equal(t, &host.ChatFrameSchedule{
		Join:      types.DefaultChatFrameJoin,
		SignEvery: types.DefaultChatFrameSignEvery,
	}, chatFrameScheduleForInference(true, types.SessionConfig{}))

	require.Equal(t, &host.ChatFrameSchedule{
		Join:      transport.MaxChatFrameJoin,
		SignEvery: transport.MaxChatFrameSignEvery,
	}, chatFrameScheduleForInference(true, types.SessionConfig{
		ChatFrameJoin:      10_000,
		ChatFrameSignEvery: 10_000,
	}))
}

func TestLogChatFrameCheck_DoneWithoutFinish(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	finish := `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"completion_tokens":1}}` + "\n\n"
	keptText, kept := gatewayChatCheck(t, `data: {"choices":[{"delta":{"content":"Hi"}}]}`+"\n\n"+finish+"data: [DONE]\n\n")
	require.Contains(t, keptText, `"finish_reason":"stop"`)
	require.Contains(t, keptText, "data: [DONE]")
	require.False(t, kept.DoneWithoutFinish)
	logChatFrameCheck(context.Background(), &inflight{
		escrowID: "escrow-1",
		nonce:    7,
		hostID:   "host-a",
		resp:     &host.HostResponse{ChatFrames: kept},
	})
	require.NotContains(t, logs.String(), "done_without_finish")

	logs.Reset()
	missedText, missed := gatewayChatCheck(t, `data: {"choices":[{"delta":{"content":"Hi"}}]}`+"\n\n"+"data: [DONE]\n\n")
	require.Contains(t, missedText, "data: [DONE]")
	require.NotContains(t, missedText, `"finish_reason":"stop"`)
	require.True(t, missed.DoneWithoutFinish)
	logChatFrameCheck(context.Background(), &inflight{
		escrowID: "escrow-1",
		nonce:    8,
		hostID:   "host-a",
		resp:     &host.HostResponse{ChatFrames: missed},
	})
	require.Contains(t, logs.String(), "done_without_finish")
	require.Contains(t, logs.String(), "escrow-1")
	require.Contains(t, logs.String(), "host-a")

	logs.Reset()
	logChatFrameCheck(context.Background(), &inflight{
		probe: true,
		resp:  &host.HostResponse{ChatFrames: missed},
	})
	require.NotContains(t, logs.String(), "done_without_finish")
}

func TestLogChatFrameCheck_NonJSONCountsInTheWarning(t *testing.T) {
	body := "data: not json\n\n"
	require.False(t, sseChunkHasContent([]byte(body)))
	inf := &inflight{}
	inf.receiptTimeNano.Store(1)
	require.True(t, isEmptyStreamAttempt(inf), "a non-JSON data line does not clear an empty stream")

	_, check := gatewayChatCheck(t, body+"data: [DONE]\n\n")
	require.Equal(t, uint64(1), check.ContentTokens)
	require.False(t, check.TokenCountMismatch)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	check.TokenCount = 0
	check.TokenCountMismatch = true
	logChatFrameCheck(context.Background(), &inflight{
		escrowID: "escrow-1",
		nonce:    3,
		hostID:   "host-a",
		resp:     &host.HostResponse{ChatFrames: check},
	})
	require.Contains(t, logs.String(), "chat_frame_token_count")
	require.Contains(t, logs.String(), "content_tokens=1")
	require.Contains(t, logs.String(), "claimed=0")
}

func gatewayChatCheck(t *testing.T, body string) (string, host.ChatFrameCheck) {
	t.Helper()
	key := testutil.MustGenerateKey(t)
	prompt := bytes.Repeat([]byte{7}, 32)
	type frame struct {
		chunk  []byte
		sig    []byte
		at     uint64
		tokens uint64
	}
	var frames []frame
	sink := transport.NewSignedChatFrameSink(func(chunk, sig []byte, at, tokens uint64) error {
		frames = append(frames, frame{
			chunk:  append([]byte(nil), chunk...),
			sig:    append([]byte(nil), sig...),
			at:     at,
			tokens: tokens,
		})
		return nil
	})
	sink.SignFrames(&host.ChatFrameSchedule{Join: 5, SignEvery: 13}, transport.NewChatFrameSigner(key, "escrow-1", 1, prompt))
	_, err := sink.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, sink.FlushErr())
	require.NoError(t, sink.Close())
	require.NotEmpty(t, frames)

	v := transport.NewChatFrameVerifier("escrow-1", 1, prompt, func(addr string) bool { return addr == key.Address() })
	rest := frames
	r := io.Reader(&frameReader{next: func() ([]byte, bool) {
		if len(rest) == 0 {
			return nil, false
		}
		f := rest[0]
		rest = rest[1:]
		v.Frame(f.sig, f.at, f.tokens)
		return f.chunk, true
	}})
	gz, err := gzip.NewReader(r)
	require.NoError(t, err)
	out, err := io.ReadAll(gz)
	require.NoError(t, err)
	v.Write(out)
	for rest := out; ; {
		i := bytes.Index(rest, []byte("\n\n"))
		if i < 0 {
			break
		}
		v.NoteEvent(rest[:i+2])
		rest = rest[i+2:]
	}
	v.Close()
	return string(out), v.Check()
}

type frameReader struct {
	next func() ([]byte, bool)
	buf  []byte
}

func (r *frameReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		chunk, ok := r.next()
		if !ok {
			return 0, io.EOF
		}
		r.buf = chunk
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

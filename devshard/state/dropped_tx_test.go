package state

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

type capturedLog struct {
	level  slog.Level
	msg    string
	fields map[string]any
}

type capturedLogs struct {
	mu    sync.Mutex
	lines []capturedLog
}

func (c *capturedLogs) warningsSaying(msg string) []capturedLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []capturedLog
	for _, line := range c.lines {
		if line.level == slog.LevelWarn && line.msg == msg {
			found = append(found, line)
		}
	}
	return found
}

// capturingHandler reads what the package logged. The logging package writes through slog's default
// handler, which the standard library already lets a test swap and restore.
type capturingHandler struct {
	captured *capturedLogs
	attrs    []slog.Attr
}

func (h capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h capturingHandler) Handle(_ context.Context, record slog.Record) error {
	fields := make(map[string]any, record.NumAttrs()+len(h.attrs))
	for _, attr := range h.attrs {
		fields[attr.Key] = attr.Value.Any()
	}
	record.Attrs(func(attr slog.Attr) bool {
		fields[attr.Key] = attr.Value.Any()
		return true
	})
	h.captured.mu.Lock()
	defer h.captured.mu.Unlock()
	h.captured.lines = append(h.captured.lines, capturedLog{
		level: record.Level, msg: record.Message, fields: fields,
	})
	return nil
}

func (h capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return h
}

func (h capturingHandler) WithGroup(string) slog.Handler { return h }

func captureLogs(t *testing.T) *capturedLogs {
	t.Helper()
	captured := &capturedLogs{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capturingHandler{captured: captured}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return captured
}

// A ConfirmStart that fails to apply is discarded and never retried, leaving the inference pending
// for good and every execution-timeout vote answering "expected started, got 0". Silence here is why
// the cause of those failures cannot be told apart in production.
func TestBestEffort_WarnsWhenItDiscardsAConfirmStart(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 1_000_000)
	captured := captureLogs(t)

	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{
		txConfirm(&types.MsgConfirmStart{InferenceId: 77, ExecutorSig: []byte("not-a-signature"), ConfirmedAt: 5}),
	})

	require.NoError(t, err, "a dropped confirm must not abort the diff")
	require.Empty(t, applied, "precondition: the confirm did not apply")

	warnings := captured.warningsSaying("dropped confirm start")
	require.Len(t, warnings, 1)
	require.Equal(t, uint64(77), warnings[0].fields["inference_id"])
	require.Equal(t, uint64(1), warnings[0].fields["nonce"])
	require.NotNil(t, warnings[0].fields["error"], "the reason is the whole point of the line")
}

// The three ways applyConfirmStart can fail are indistinguishable from outside, so the reason has to
// travel with the line.
func TestBestEffort_CarriesTheReasonTheConfirmWasDiscarded(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 1_000_000)
	captured := captureLogs(t)

	_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{
		txConfirm(&types.MsgConfirmStart{InferenceId: 404, ExecutorSig: []byte("x"), ConfirmedAt: 1}),
	})
	require.NoError(t, err)

	warnings := captured.warningsSaying("dropped confirm start")
	require.Len(t, warnings, 1)
	require.Contains(t, fmt.Sprint(warnings[0].fields["error"]), "404",
		"the message must name the inference that went missing")
}

// Gossiped mempool transactions arrive over and over and stale ones are ordinary, so warning on them
// would bury the confirms this exists to surface.
func TestBestEffort_DoesNotWarnOnOrdinaryStaleTransactions(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 1_000_000)
	captured := captureLogs(t)

	_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{
		txFinish(&types.MsgFinishInference{InferenceId: 9, OutputTokens: 1}),
	})
	require.NoError(t, err)

	require.Empty(t, captured.warningsSaying("dropped confirm start"))
	require.Empty(t, captured.warningsSaying("dropped tx"))
}

func TestBestEffort_KeepsQuietWhenNothingIsDiscarded(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, _ := newTestSM(t, hosts, 1_000_000)
	captured := captureLogs(t)

	_, applied, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{
		txStart(&types.MsgStartInference{
			InferenceId: 1,
			PromptHash:  []byte("prompt"),
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   testutil.TestMaxTokens,
			StartedAt:   1000,
		}),
	})
	require.NoError(t, err)
	require.Len(t, applied, 1)

	require.Empty(t, captured.warningsSaying("dropped confirm start"))
}

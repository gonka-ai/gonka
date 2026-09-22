package host

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/logging"
	"devshard/types"
)

type capturingLogger struct {
	mu      sync.Mutex
	records []capturedLog
}

type capturedLog struct {
	level string
	msg   string
	kv    []any
}

func (c *capturingLogger) append(level, msg string, kv []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dup := make([]any, len(kv))
	copy(dup, kv)
	c.records = append(c.records, capturedLog{level: level, msg: msg, kv: dup})
}

func (c *capturingLogger) Info(msg string, kv ...any)  { c.append("info", msg, kv) }
func (c *capturingLogger) Error(msg string, kv ...any) { c.append("error", msg, kv) }
func (c *capturingLogger) Warn(msg string, kv ...any)  { c.append("warn", msg, kv) }
func (c *capturingLogger) Debug(msg string, kv ...any) { c.append("debug", msg, kv) }

func (c *capturingLogger) find(msg string) (capturedLog, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rec := range c.records {
		if rec.msg == msg {
			return rec, true
		}
	}
	return capturedLog{}, false
}

func kvInt64(t *testing.T, kv []any, key string) int64 {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok || k != key {
			continue
		}
		switch v := kv[i+1].(type) {
		case int64:
			return v
		case int:
			return int64(v)
		case uint64:
			return int64(v)
		default:
			t.Fatalf("key %s: unexpected type %T", key, kv[i+1])
		}
	}
	t.Fatalf("missing key %s", key)
	return 0
}

func TestHost_ValidateInferenceDisappearedLogsFinishObs(t *testing.T) {
	h, hosts, user := newLeaseReleaseHost(t, &scriptedValidationEngine{}, nil)
	applyInferenceTo(t, h, hosts, user, types.StatusFinished)

	h.mu.Lock()
	obs, ok := h.finishObs[1]
	h.mu.Unlock()
	require.True(t, ok, "finish obs should be recorded when MsgFinishInference applies")
	require.Equal(t, uint64(3), obs.nonce)
	require.False(t, obs.at.IsZero())

	require.NoError(t, h.sm.SealInference(1))
	_, stillLive := h.sm.GetInference(1)
	require.False(t, stillLive)

	capLog := &capturingLogger{}
	logging.SetLogger(capLog)
	t.Cleanup(func() { logging.SetLogger(loggingDiscard{}) })

	before := time.Now().Unix()
	h.validateAsync(context.Background(), testValidateJob())
	rec, found := capLog.find("validate: inference disappeared")
	require.True(t, found)
	require.Equal(t, "error", rec.level)
	require.Equal(t, int64(3), kvInt64(t, rec.kv, "finish_nonce"))
	require.Equal(t, obs.at.Unix(), kvInt64(t, rec.kv, "finish_at"))
	require.GreaterOrEqual(t, kvInt64(t, rec.kv, "current_nonce"), int64(3))
	require.GreaterOrEqual(t, kvInt64(t, rec.kv, "current_at"), before)
	require.GreaterOrEqual(t, kvInt64(t, rec.kv, "current_at"), kvInt64(t, rec.kv, "finish_at"))
}

type loggingDiscard struct{}

func (loggingDiscard) Info(string, ...any)  {}
func (loggingDiscard) Error(string, ...any) {}
func (loggingDiscard) Warn(string, ...any)  {}
func (loggingDiscard) Debug(string, ...any) {}

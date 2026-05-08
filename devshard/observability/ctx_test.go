package observability

import (
	"context"
	"net/http"
	"testing"

	"devshard/logging"

	"github.com/stretchr/testify/require"
)

type captureLogger struct {
	msg string
	kv  []any
}

func (l *captureLogger) Info(msg string, keyvals ...any) {
	l.msg = msg
	l.kv = append([]any(nil), keyvals...)
}
func (l *captureLogger) Warn(string, ...any)  {}
func (l *captureLogger) Error(string, ...any) {}
func (l *captureLogger) Debug(string, ...any) {}

func TestRecordTerminalIncludesFailureWhere(t *testing.T) {
	logger := &captureLogger{}
	logging.SetLogger(logger)
	SetRuntime("api", "v1", "dapi_inprocess")

	ctx := logging.WithRequestID(context.Background(), "req-test")
	RecordExecutionNoFinish(ctx, "escrow-1", 7, 3, ReasonHTTP5xx, WhereEngineMLNodeCall)

	fields := keyvals(logger.kv)
	require.Equal(t, "devshard request terminal", logger.msg)
	require.Equal(t, "req-test", fields["request_id"])
	require.Equal(t, "terminal", fields["stage"])
	require.Equal(t, "request.terminal", fields["where"])
	require.Equal(t, "execution_no_finish", fields["terminal"])
	require.Equal(t, "http_5xx", fields["reason"])
	require.Equal(t, "engine.mlnode_call", fields["failure_where"])
	require.Equal(t, uint64(7), fields["inference_id"])
	require.Equal(t, true, fields["receipt_observed"])
	require.Equal(t, false, fields["finish_published"])
}

func TestInterruptionClass(t *testing.T) {
	require.Equal(t, ReasonNoReceipt, interruptionClass(TerminalNoReceiptInterrupted))
	require.Equal(t, ReasonReceiptNoExecution, interruptionClass(TerminalReceiptNoExecutionInterrupted))
	require.Equal(t, ReasonExecutionNoFinish, interruptionClass(TerminalExecutionNoFinish))
	require.Equal(t, ReasonExecutionNoFinish, interruptionClass(TerminalClientCancelledAfterReceipt))
	require.Empty(t, interruptionClass(TerminalFinishPublished))
}

func TestAttachRequestID(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "req-outbound")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mlnode/v1/chat/completions", nil)
	require.NoError(t, err)

	AttachRequestID(req)

	require.Equal(t, "req-outbound", req.Header.Get(RequestIDHeader))
}

func keyvals(kv []any) map[string]any {
	fields := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		fields[key] = kv[i+1]
	}
	return fields
}

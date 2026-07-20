package observability

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestClassifyMLNodeHTTP(t *testing.T) {
	transport := errors.New("dial: connection refused")

	tests := []struct {
		name    string
		resp    *http.Response
		postErr error
		ctxErr  error
		want    Reason
	}{
		{"transport error without ctx cancel", nil, transport, nil, ReasonTransportErr},
		{"transport error with ctx cancel", nil, transport, context.DeadlineExceeded, ReasonTimeout},
		{"nil resp without postErr is transport", nil, nil, nil, ReasonTransportErr},
		{"5xx", &http.Response{StatusCode: 503}, nil, nil, ReasonHTTP5xx},
		{"4xx", &http.Response{StatusCode: 422}, nil, nil, ReasonHTTP4xx},
		{"2xx", &http.Response{StatusCode: 200}, nil, nil, ReasonOK},
		{"3xx classified as ok", &http.Response{StatusCode: 304}, nil, nil, ReasonOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyMLNodeHTTP(tc.resp, tc.postErr, tc.ctxErr)
			if got != tc.want {
				t.Fatalf("ClassifyMLNodeHTTP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIncMLNodeAttemptIncrementsCounter(t *testing.T) {
	ensureMetrics()
	const nodeID = "test-node-attempt"
	before := testutil.ToFloat64(mlnodeAttemptsTotal.WithLabelValues(string(PathExecute), string(ReasonOK), nodeID))
	IncMLNodeAttempt(PathExecute, ReasonOK, nodeID)
	after := testutil.ToFloat64(mlnodeAttemptsTotal.WithLabelValues(string(PathExecute), string(ReasonOK), nodeID))
	if after-before != 1 {
		t.Fatalf("counter delta = %v, want 1", after-before)
	}
}

func TestIncReceiptOrphanIncrementsCounter(t *testing.T) {
	ensureMetrics()
	before := testutil.ToFloat64(receiptOrphanTotal.WithLabelValues(string(ReasonExecutionNoFinish)))
	IncReceiptOrphan(ReasonExecutionNoFinish)
	after := testutil.ToFloat64(receiptOrphanTotal.WithLabelValues(string(ReasonExecutionNoFinish)))
	if after-before != 1 {
		t.Fatalf("counter delta = %v, want 1", after-before)
	}
}

func TestIncValidationOrphanIncrementsCounter(t *testing.T) {
	ensureMetrics()
	before := testutil.ToFloat64(validationOrphanTotal.WithLabelValues(string(ReasonValidateErr)))
	IncValidationOrphan(ReasonValidateErr)
	after := testutil.ToFloat64(validationOrphanTotal.WithLabelValues(string(ReasonValidateErr)))
	if after-before != 1 {
		t.Fatalf("counter delta = %v, want 1", after-before)
	}
}

func TestSetValidationDeferredDepthWarnAbove100(t *testing.T) {
	ensureMetrics()
	deferredDepthWarnMu.Lock()
	deferredDepthWasAbove = false
	var warns int
	deferredDepthWarnHook = func(int) { warns++ }
	deferredDepthWarnMu.Unlock()
	t.Cleanup(func() {
		deferredDepthWarnMu.Lock()
		deferredDepthWarnHook = nil
		deferredDepthWasAbove = false
		deferredDepthWarnMu.Unlock()
	})

	SetValidationDeferredDepth(100)
	if warns != 0 {
		t.Fatalf("warn at threshold boundary: got %d, want 0", warns)
	}
	SetValidationDeferredDepth(101)
	if warns != 1 {
		t.Fatalf("warn on cross above 100: got %d, want 1", warns)
	}
	SetValidationDeferredDepth(150)
	if warns != 1 {
		t.Fatalf("no re-warn while still above: got %d, want 1", warns)
	}
	SetValidationDeferredDepth(50)
	if warns != 1 {
		t.Fatalf("no warn on drop: got %d, want 1", warns)
	}
	SetValidationDeferredDepth(101)
	if warns != 2 {
		t.Fatalf("warn again after recovery: got %d, want 2", warns)
	}

	if got := testutil.ToFloat64(validationDeferredDepth); got != 101 {
		t.Fatalf("gauge = %v, want 101", got)
	}
}

func TestSetValidationPendingAndOutstandingGauges(t *testing.T) {
	ensureMetrics()
	SetValidationPendingDepth(7)
	SetValidationOutstanding(3)
	SetValidationSoftMaxOut(128)
	SetValidationEffectiveSoftMaxOut(32)
	IncValidationAcquireBusyRequeue()

	if got := testutil.ToFloat64(validationPendingDepth); got != 7 {
		t.Fatalf("pending = %v, want 7", got)
	}
	if got := testutil.ToFloat64(validationOutstanding); got != 3 {
		t.Fatalf("outstanding = %v, want 3", got)
	}
	if got := testutil.ToFloat64(validationSoftMaxOut); got != 128 {
		t.Fatalf("soft_max = %v, want 128", got)
	}
	if got := testutil.ToFloat64(validationEffectiveSoftMaxOut); got != 32 {
		t.Fatalf("effective_soft_max = %v, want 32", got)
	}
}

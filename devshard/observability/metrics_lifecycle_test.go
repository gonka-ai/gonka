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

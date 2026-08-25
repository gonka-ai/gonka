package observability

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
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

func TestObservePayloadFetchTTFB(t *testing.T) {
	ensureMetrics()
	var m dto.Metric
	if err := payloadFetchTTFB.Write(&m); err != nil {
		t.Fatal(err)
	}
	before := m.Histogram.GetSampleCount()
	ObservePayloadFetchTTFB(25 * time.Millisecond)
	m.Reset()
	if err := payloadFetchTTFB.Write(&m); err != nil {
		t.Fatal(err)
	}
	if got := m.Histogram.GetSampleCount() - before; got != 1 {
		t.Fatalf("histogram sample delta = %d, want 1", got)
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

func TestHADiffPersistMetricsIncrement(t *testing.T) {
	ensureMetrics()

	beforeFork := testutil.ToFloat64(diffForkDetectedTotal.WithLabelValues("esc-metrics"))
	IncDiffForkDetected("esc-metrics")
	if testutil.ToFloat64(diffForkDetectedTotal.WithLabelValues("esc-metrics"))-beforeFork != 1 {
		t.Fatalf("diff_fork_detected delta want 1")
	}

	beforeRetry := testutil.ToFloat64(diffPersistRetryTotal.WithLabelValues("success"))
	IncDiffPersistRetry("success")
	if testutil.ToFloat64(diffPersistRetryTotal.WithLabelValues("success"))-beforeRetry != 1 {
		t.Fatalf("diff_persist_retry delta want 1")
	}

	beforeFF := testutil.ToFloat64(reconcileFastForwardTotal)
	IncReconcileFastForward()
	if testutil.ToFloat64(reconcileFastForwardTotal)-beforeFF != 1 {
		t.Fatalf("reconcile_fast_forward delta want 1")
	}
}

func TestDeleteEscrowMetricsRemovesPerEscrowGauges(t *testing.T) {
	ensureMetrics()
	const escrowID = "escrow-metrics-prune"

	SetValidationQueueDepth(escrowID, 3)
	SetMempoolSize(escrowID, 7)

	DeleteEscrowMetrics(escrowID)

	mf, err := Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, family := range mf {
		switch family.GetName() {
		case "devshard_validation_queue_depth", "devshard_mempool_size":
			for _, m := range family.Metric {
				for _, lp := range m.Label {
					if lp.GetName() == "escrow_id" && lp.GetValue() == escrowID {
						t.Fatalf("%s still has escrow_id=%q after DeleteEscrowMetrics", family.GetName(), escrowID)
					}
				}
			}
		}
	}
}

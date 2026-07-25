package transport

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"devshard/observability"
)

// TestServer_Inference_OwnerErr_RecordsTerminal locks in that the
// FailNoReceipt path emits a request-terminal counter increment for the
// owner-mismatch branch of HandleInference. Regression guard for the helper
// extraction in transport/server.go.
func TestServer_Inference_OwnerErr_RecordsTerminal(t *testing.T) {
	env := setupServerEnv(t)
	counter := observability.RequestTerminalCounterForTest(
		observability.TerminalNoReceiptInterrupted, observability.ReasonOwnerErr)
	before := testutil.ToFloat64(counter)

	body := []byte(`{}`)
	rec := env.doPostAs(t, "/devshard/v2/sessions/escrow-1/chat/completions", body, env.hostSigner)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	after := testutil.ToFloat64(counter)
	if after-before != 1 {
		t.Fatalf("request_terminal_total{terminal=no_receipt_interrupted,reason=owner_err} delta = %v, want 1",
			after-before)
	}
}

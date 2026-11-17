package shadow

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/user"
)

const (
	silentFor    = 100 * time.Millisecond
	slowGap      = 20 * time.Millisecond
	slowChunks   = 10
	shortHardCut = 30 * time.Millisecond
)

func hardTimeoutUnderTightEngineBounds() time.Duration {
	settings := config.Engine{ReceiptTimeoutMS: 1, InterChunkStallMS: 1}
	return engine.PolicyFor(settings, engine.E2EOverrides{}).HardTimeoutOrDefault()
}

// Test flow:
//  1. Bind a runner with the hard bound of an engine whose receipt bound is a millisecond, whose send sends no receipt and no chunk for a while, then receipts, streams a chunk and ends.
//  2. Probe a committed nonce and await the reported result.
//  3. Assert the send's context was still live after the silence, the probe was served and its ledger line is acknowledged.
func TestProbeSilentBeforeItsReceiptIsNotCut(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	var afterSilence error
	fixture := newFixture(t, func(ctx context.Context, _ registry.EscrowSession, _ *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
		time.Sleep(silentFor)
		afterSilence = ctx.Err()
		onReceipt()
		_, _ = answer.Write([]byte(contentEvent))
		return &host.HostResponse{Receipt: []byte("receipt")}, nil
	})
	fixture.hardTime = hardTimeoutUnderTightEngineBounds()

	fixture.runner.Probe(fixture.request())
	result := fixture.await(t)

	if afterSilence != nil {
		t.Errorf("send context after %s of silence = %v, want live", silentFor, afterSilence)
	}
	if result.Outcome != OutcomeServed || result.Err != nil {
		t.Fatalf("result = %+v, want a served probe", result)
	}
	if len(fixture.probes.attempts) != 1 || !fixture.probes.attempts[0].Acknowledged {
		t.Errorf("ledger attempts = %+v, want one acknowledged attempt", fixture.probes.attempts)
	}
}

// Test flow:
//  1. Bind a runner with the hard bound of an engine whose receipt and stall bounds are a millisecond, whose send receipts, streams a chunk, goes silent for a while and then streams slowly.
//  2. Probe a committed nonce and await the reported result.
//  3. Assert the send's context stayed live through the silence and the slow chunks, the probe was served, and its deadline is the engine's, well past two minutes.
func TestProbeStreamingSlowlyIsNotCut(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	var remaining time.Duration
	cutAt := -1
	fixture := newFixture(t, func(ctx context.Context, _ registry.EscrowSession, _ *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
		deadline, _ := ctx.Deadline()
		remaining = time.Until(deadline)
		onReceipt()
		_, _ = answer.Write([]byte(contentEvent))
		time.Sleep(silentFor)
		for chunk := range slowChunks {
			if ctx.Err() != nil && cutAt < 0 {
				cutAt = chunk
			}
			time.Sleep(slowGap)
			if _, err := answer.Write([]byte(contentEvent)); err != nil {
				return nil, err
			}
		}
		return &host.HostResponse{Receipt: []byte("receipt")}, nil
	})
	fixture.hardTime = hardTimeoutUnderTightEngineBounds()

	fixture.runner.Probe(fixture.request())
	result := fixture.await(t)

	if cutAt >= 0 {
		t.Errorf("send context was cancelled before chunk %d, want live to the end", cutAt)
	}
	if result.Outcome != OutcomeServed || result.Err != nil {
		t.Fatalf("result = %+v, want a served probe", result)
	}
	if remaining <= 2*time.Minute {
		t.Errorf("send deadline in %s, want the engine's hard timeout, well past two minutes", remaining)
	}
}

// Test flow:
//  1. Bind a runner with a short hard bound whose send blocks until its context ends, with no receipt in one case and a receipt and a chunk in the other.
//  2. Probe a committed nonce and await the reported result and its vote.
//  3. Assert the probe failed with the deadline as its error and was voted as a refused timeout without a receipt and as an execution timeout with one.
func TestProbePastTheHardBoundIsCutAndVoted(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		receipts bool
		wantKind engine.TimeoutKind
	}{
		{name: "no receipt", wantKind: engine.TimeoutKindRefused},
		{name: "receipted", receipts: true, wantKind: engine.TimeoutKindExecution},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			defer leakcheck.VerifyNoneStarted(t)()
			fixture := newFixture(t, func(ctx context.Context, _ registry.EscrowSession, _ *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
				if testCase.receipts {
					onReceipt()
					_, _ = answer.Write([]byte(contentEvent))
				}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			fixture.hardTime = shortHardCut

			fixture.runner.Probe(fixture.request())
			result := fixture.await(t)

			if result.Outcome != OutcomeFailed || !errors.Is(result.Err, context.DeadlineExceeded) {
				t.Fatalf("result = %+v, want a failed probe cut by the hard bound", result)
			}
			if len(fixture.probes.attempts) != 1 || fixture.probes.attempts[0].Acknowledged != testCase.receipts {
				t.Errorf("ledger attempts = %+v, want one attempt acknowledged = %t", fixture.probes.attempts, testCase.receipts)
			}
			if len(fixture.timeouts.events) == 0 || fixture.timeouts.events[0].Kind != testCase.wantKind {
				t.Errorf("timeout events = %+v, want a %v vote", fixture.timeouts.events, testCase.wantKind)
			}
		})
	}
}

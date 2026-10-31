package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"devshard/cmd/gateway/metrics"
	"devshard/user"
)

type recordedMismatches struct {
	struck   []string
	narrated []uint64
	finished []uint64
}

func (r *recordedMismatches) OnFinishApplied(_ string, nonce uint64) {
	r.finished = append(r.finished, nonce)
}

func (r *recordedMismatches) RecordServedMismatch(participant, model string) {
	r.struck = append(r.struck, participant+"/"+model)
}

func (r *recordedMismatches) HostServedUnsigned(_, _, _ string, nonce uint64) {
	r.narrated = append(r.narrated, nonce)
}

// Test flow:
//  1. Report one bound and one mismatched served stream for the same host.
//  2. Assert both verdicts are counted, and only the mismatch is narrated and struck against the host on the escrow's model.
func TestAMismatchedServedStreamStrikesItsHost(t *testing.T) {
	mismatches := &recordedMismatches{}
	telemetry := metrics.New()
	observe := servedBindingStrikes(mismatches, mismatches, metrics.NewServedBindingRecorder(telemetry), mismatches)

	observe("escrow-1", "llama", "host-a", 7, user.ServedBindingBound)
	observe("escrow-1", "llama", "host-a", 8, user.ServedBindingMismatch)

	if !slices.Equal(mismatches.struck, []string{"host-a/llama"}) || !slices.Equal(mismatches.narrated, []uint64{8}) {
		t.Fatalf("RecordServedMismatch = %v, HostServedUnsigned = %v, want host-a/llama struck and nonce 8 narrated once", mismatches.struck, mismatches.narrated)
	}
	expected := `
# HELP devshard_gateway_served_bindings_total Total served streams checked against the Finish their host signed, by verdict.
# TYPE devshard_gateway_served_bindings_total counter
devshard_gateway_served_bindings_total{model="llama",verdict="bound"} 1
devshard_gateway_served_bindings_total{model="llama",verdict="mismatch"} 1
`
	if err := testutil.GatherAndCompare(telemetry.Registry(), strings.NewReader(expected), "devshard_gateway_served_bindings_total"); err != nil {
		t.Fatalf("GatherAndCompare(served bindings) = %v, want nil", err)
	}
}

// Test flow:
//  1. Report one bound and one mismatched served stream on escrow-1.
//  2. Assert both nonces reach the engine as applied finishes, in the order their bindings were reported.
func TestEveryServedBindingReportsItsFinishApplied(t *testing.T) {
	observed := &recordedMismatches{}
	observe := servedBindingStrikes(observed, observed, metrics.NewServedBindingRecorder(metrics.New()), observed)

	observe("escrow-1", "llama", "host-a", 7, user.ServedBindingBound)
	observe("escrow-1", "llama", "host-a", 8, user.ServedBindingMismatch)

	if !slices.Equal(observed.finished, []uint64{7, 8}) {
		t.Fatalf("OnFinishApplied = %v, want nonces 7 and 8", observed.finished)
	}
}

// Test flow:
//  1. Report a served binding through an observer wired with no finish notice, and through one whose notice has no engine attached yet.
//  2. Assert both still count the verdict.
func TestAServedBindingBeforeTheEngineExistsOnlyCountsItsVerdict(t *testing.T) {
	telemetry := metrics.New()
	recorder := metrics.NewServedBindingRecorder(telemetry)
	mismatches := &recordedMismatches{}
	var missing *finishNotice

	servedBindingStrikes(mismatches, mismatches, recorder, missing)("escrow-1", "llama", "host-a", 7, user.ServedBindingBound)
	servedBindingStrikes(mismatches, mismatches, recorder, &finishNotice{})("escrow-1", "llama", "host-a", 8, user.ServedBindingBound)

	expected := `
# HELP devshard_gateway_served_bindings_total Total served streams checked against the Finish their host signed, by verdict.
# TYPE devshard_gateway_served_bindings_total counter
devshard_gateway_served_bindings_total{model="llama",verdict="bound"} 2
`
	if err := testutil.GatherAndCompare(telemetry.Registry(), strings.NewReader(expected), "devshard_gateway_served_bindings_total"); err != nil {
		t.Fatalf("GatherAndCompare(served bindings) = %v, want nil", err)
	}
}

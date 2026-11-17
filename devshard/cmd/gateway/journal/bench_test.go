package journal

import (
	"testing"
	"time"

	"devshard/types"
)

// sinkFields keeps benchmark results from being optimized away.
var sinkFields []any

// discardLines is a Lines sink that discards everything.
type discardLines struct{}

func (discardLines) Info(string, ...any)  {}
func (discardLines) Warn(string, ...any)  {}
func (discardLines) Error(string, ...any) {}

// BenchmarkAttemptFinishFields benchmarks building the fields for a finished attempt.
func BenchmarkAttemptFinishFields(b *testing.B) {
	step := widestFinishedStep()

	b.ReportAllocs()
	for b.Loop() {
		sinkFields = appendAttemptDeliveryFields(attemptFinishHead(&step), &step.Outcome)
	}
}

// BenchmarkRecordStep benchmarks recording a step through the journal.
func BenchmarkRecordStep(b *testing.B) {
	events := New(Settings{Lines: discardLines{}})
	b.Cleanup(func() { _ = events.Close() })
	step := widestFinishedStep()

	b.ReportAllocs()
	for b.Loop() {
		events.RecordStep(step)
	}
}

// BenchmarkRequestFinishedFields benchmarks building the fields for a finished request.
func BenchmarkRequestFinishedFields(b *testing.B) {
	line := RequestLine{
		RequestID: "request-1", Model: "qwen", ClientStream: true, Outcome: servedOutcome(),
		Verdict: "served", Elapsed: 3 * time.Second,
	}

	b.ReportAllocs()
	for b.Loop() {
		sinkFields = requestFinishedFields(&line)
	}
}

// BenchmarkDiffComposed benchmarks recording a composed diff that carries a ledger fact.
func BenchmarkDiffComposed(b *testing.B) {
	events := New(Settings{Lines: discardLines{}})
	b.Cleanup(func() { _ = events.Close() })
	diff := composedDiff()

	b.ReportAllocs()
	for b.Loop() {
		events.DiffComposed("escrow-1", diff)
	}
}

// BenchmarkDiffComposedWithoutFacts benchmarks recording a composed diff that carries no ledger fact.
func BenchmarkDiffComposedWithoutFacts(b *testing.B) {
	events := New(Settings{Lines: discardLines{}})
	b.Cleanup(func() { _ = events.Close() })
	diff := &types.Diff{Nonce: 9, Txs: []*types.DevshardTx{
		{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 9}}},
	}}

	b.ReportAllocs()
	for b.Loop() {
		events.DiffComposed("escrow-1", diff)
	}
}

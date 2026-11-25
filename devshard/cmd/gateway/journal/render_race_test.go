package journal

import (
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
)

var renderEpoch = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

func loggedValue(fields []any, name string) (any, bool) {
	for index := 0; index+1 < len(fields); index += 2 {
		if fields[index] == name {
			return fields[index+1], true
		}
	}
	return nil, false
}

// Test flow:
//  1. Build an `engine.AttemptOutcome` with send, receipt, first-token, and completion times plus chunk and usage counts set.
//  2. Render its delivery fields via `appendAttemptDeliveryFields`.
//  3. Assert each expected field ("content_chunks", "stream_chunks", "usage_tokens", "receipt_ms", "first_token_ms", "attempt_ms") holds the expected value.
func TestAttemptDeliveryFields_ReportWhatTheHostReturned(t *testing.T) {
	t.Parallel()
	dispatchedAt := time.Unix(1786114580, 0)

	fields := appendAttemptDeliveryFields(nil, &engine.AttemptOutcome{
		SendTime:              dispatchedAt,
		ReceiptTime:           dispatchedAt.Add(120 * time.Millisecond),
		FirstToken:            dispatchedAt.Add(2 * time.Second),
		Completed:             dispatchedAt.Add(9 * time.Second),
		ContentChunks:         41,
		StreamChunks:          43,
		UsageCompletionTokens: 64,
	})

	for _, expected := range []struct {
		name  string
		value any
	}{
		{"content_chunks", int64(41)},
		{"stream_chunks", int64(43)},
		{"usage_tokens", int64(64)},
		{"receipt_ms", int64(120)},
		{"first_token_ms", int64(2000)},
		{"attempt_ms", int64(9000)},
	} {
		got, held := loggedValue(fields, expected.name)
		if !held {
			t.Errorf("%s missing", expected.name)
			continue
		}
		if got != expected.value {
			t.Errorf("%s = %v, want %v", expected.name, got, expected.value)
		}
	}
}

// Test flow:
//  1. Build an `engine.AttemptOutcome` with only `SendTime` and `Completed` set, so receipt and first-token never happened.
//  2. Render its delivery fields via `appendAttemptDeliveryFields`.
//  3. Assert "receipt_ms" and "first_token_ms" are absent.
//  4. Assert "attempt_ms" still reports the elapsed time to completion.
func TestAttemptDeliveryFields_SkipAStageThatNeverHappened(t *testing.T) {
	t.Parallel()
	dispatchedAt := time.Unix(1786114580, 0)

	fields := appendAttemptDeliveryFields(nil, &engine.AttemptOutcome{
		SendTime:  dispatchedAt,
		Completed: dispatchedAt.Add(3 * time.Second),
	})

	for _, absent := range []string{"receipt_ms", "first_token_ms"} {
		if _, held := loggedValue(fields, absent); held {
			t.Errorf("%s reported for a stage that never happened", absent)
		}
	}
	if got, _ := loggedValue(fields, "attempt_ms"); got != int64(3000) {
		t.Errorf("attempt_ms = %v, want 3000", got)
	}
}

// Test flow:
//  1. Build an `engine.AttemptOutcome` with only `Completed` and `StreamChunks` set, with no dispatch (`SendTime`) recorded.
//  2. Render its delivery fields via `appendAttemptDeliveryFields`.
//  3. Assert "receipt_ms", "first_token_ms", and "attempt_ms" are all absent.
//  4. Assert "stream_chunks" still reports its value.
func TestAttemptDeliveryFields_SkipEveryDurationWithoutADispatch(t *testing.T) {
	t.Parallel()

	fields := appendAttemptDeliveryFields(nil, &engine.AttemptOutcome{Completed: time.Unix(1786114580, 0), StreamChunks: 2})

	for _, absent := range []string{"receipt_ms", "first_token_ms", "attempt_ms"} {
		if _, held := loggedValue(fields, absent); held {
			t.Errorf("%s reported without a dispatch to measure from", absent)
		}
	}
	if got, _ := loggedValue(fields, "stream_chunks"); got != int64(2) {
		t.Errorf("stream_chunks = %v, want 2", got)
	}
}

// widestFinishedStep carries every field a finish line can report, so the line it builds is the widest one.
func widestFinishedStep() engine.RaceStep {
	return engine.RaceStep{
		Kind: engine.RaceStepAttemptFinished, RequestID: "request-1", EscrowID: "escrow-1", Nonce: 77,
		Participant: "host-3", Terminal: engine.TerminalLost, NonceFinished: true, HasOutcome: true, PhaseAborted: true,
		Outcome: engine.AttemptOutcome{
			SendTime:     renderEpoch,
			ReceiptTime:  renderEpoch.Add(200 * time.Millisecond),
			FirstToken:   renderEpoch.Add(900 * time.Millisecond),
			FirstContent: renderEpoch.Add(950 * time.Millisecond),
			Completed:    renderEpoch.Add(9 * time.Second),

			ContentChunks: 412, StreamChunks: 415, OutputBytes: 61_440,
			UsageCompletionTokens: 512,
			MaxChunkGap:           1200 * time.Millisecond, MaxChunkGapAt: 310,
			MeanChunkGap:   21 * time.Millisecond,
			UpstreamStatus: 502, UpstreamBody: "upstream is down",
			LastChunkHead:     `data: {"choices":[],"usage":{"completion_tokens":0}}`,
			FirstChunkHead:    `data: {"choices":[{"delta":{}}],"prompt_token_ids":[...]}`,
			FinishReason:      "length",
			UsagePromptTokens: 49_019,
			LogprobTokens:     5,

			ReceiptDeadlineMissed: true, FirstTokenDeadlineMissed: true,
		},
	}
}

// Test flow:
//  1. Build the `widestFinishedStep` fixture, the widest finish line the code can build.
//  2. Render its fields via `attemptFinishHead`, `appendAttemptDeliveryFields`, and `appendAttemptMarks`.
//  3. Assert the number of fields produced equals `attemptFinishFields`, the hand-counted reservation.
func TestAFinishLineFitsWhatItReserves(t *testing.T) {
	t.Parallel()
	step := widestFinishedStep()

	fields := appendAttemptMarks(appendAttemptDeliveryFields(attemptFinishHead(&step), &step.Outcome), &step)

	if len(fields) != attemptFinishFields {
		t.Fatalf("the widest finish line is %d fields, but %d are reserved", len(fields), attemptFinishFields)
	}
}

// Test flow:
//  1. Build a `RaceStep` whose outcome missed only the first-token deadline.
//  2. Render its deadline marks via `appendAttemptMarks`.
//  3. Assert "missed_first_token_deadline" is true and "missed_receipt_deadline" is absent.
func TestAFinishLineNamesOnlyTheDeadlinesItsHostMissed(t *testing.T) {
	t.Parallel()
	step := engine.RaceStep{HasOutcome: true, Outcome: engine.AttemptOutcome{FirstTokenDeadlineMissed: true}}

	fields := appendAttemptMarks(nil, &step)

	if got, _ := loggedValue(fields, "missed_first_token_deadline"); got != true {
		t.Errorf("missed_first_token_deadline = %v, want true", got)
	}
	if _, held := loggedValue(fields, "missed_receipt_deadline"); held {
		t.Error("missed_receipt_deadline reported for a deadline the host met")
	}
}

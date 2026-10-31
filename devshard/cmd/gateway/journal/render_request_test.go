package journal

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logkey"
)

// servedOutcome is a race one host won, with the timings and the stamp a finished request is logged from.
func servedOutcome() engine.RaceOutcome {
	sent := time.Unix(1700000000, 0)
	return engine.RaceOutcome{
		Model: "qwen", InputTokens: 1024, ClientStream: true, WinnerNonce: 7, Succeeded: true,
		Attempts: []engine.AttemptOutcome{{
			Participant:           "gonka1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq",
			HostIdx:               1,
			HostLabel:             "host-1",
			Nonce:                 7,
			SendTime:              sent,
			ReceiptTime:           sent.Add(200 * time.Millisecond),
			FirstToken:            sent.Add(400 * time.Millisecond),
			Completed:             sent.Add(3 * time.Second),
			UsageCompletionTokens: 384,
			Confirmed:             true,
			ConfirmedAt:           sent.Add(100 * time.Millisecond).Unix(),
		}},
	}
}

// fieldValue reads one keyval out of a built log line.
func fieldValue(fields []any, key string) any {
	for index := 0; index+1 < len(fields); index += 2 {
		if name, ok := fields[index].(string); ok && name == key {
			return fields[index+1]
		}
	}
	return nil
}

// Test flow:
//  1. Build a `RequestLine` from `servedOutcome` with both a race error and a deliver error set.
//  2. Render its fields via `requestFinishedFields`.
//  3. Assert the field count equals `loggedFieldSlots`, the widest finished-request line's hand-counted reservation.
func TestARequestFinishedLineFitsWhatItReserves(t *testing.T) {
	line := RequestLine{
		RequestID: "request-1", Model: "qwen", EscrowID: "escrow-1", ClientStream: true, Outcome: servedOutcome(),
		Verdict: "served", Elapsed: 3 * time.Second, RaceErr: errors.New("race"), DeliverErr: errors.New("deliver"),
	}

	fields := requestFinishedFields(&line)

	require.Len(t, fields, loggedFieldSlots, "the widest finished-request line must fill its reservation exactly")
}

// Test flow:
//  1. Build a `RequestLine` from `servedOutcome` whose winning attempt never closed its nonce.
//  2. Render its fields via `requestFinishedFields`.
//  3. Assert the `logkey.NonceFinished` field is false.
func TestARequestFinishedLineSaysTheWinnersNonceNeverClosed(t *testing.T) {
	line := RequestLine{RequestID: "request-1", Model: "qwen", Outcome: servedOutcome(), Verdict: "served", Elapsed: time.Second}

	fields := requestFinishedFields(&line)

	require.Equal(t, false, fieldValue(fields, logkey.NonceFinished),
		"a served request whose nonce never closed must say so where an operator reads it")
}

// Test flow:
//  1. Build a `RequestLine` from `servedOutcome` with the winning attempt's `NonceFinished` set to true.
//  2. Render its fields via `requestFinishedFields`.
//  3. Assert the `logkey.NonceFinished` field is true.
func TestARequestFinishedLineSaysTheWinnersNonceClosed(t *testing.T) {
	outcome := servedOutcome()
	outcome.Attempts[0].NonceFinished = true
	line := RequestLine{RequestID: "request-1", Model: "qwen", Outcome: outcome, Verdict: "served", Elapsed: time.Second}

	fields := requestFinishedFields(&line)

	require.Equal(t, true, fieldValue(fields, logkey.NonceFinished))
}

// Test flow:
//  1. Build a `RequestLine` for a request that failed before any winner was crowned.
//  2. Render its fields via `requestFinishedFields`.
//  3. Assert the `logkey.NonceFinished` field is absent.
func TestARequestFinishedLineOmitsTheNonceNobodyWon(t *testing.T) {
	line := RequestLine{
		RequestID: "request-1", Model: "qwen", Verdict: "failed_before_first_byte", Elapsed: time.Second,
		Outcome: engine.RaceOutcome{Model: "qwen", Attempts: []engine.AttemptOutcome{{Participant: "gonka1a"}}},
		RaceErr: errors.New("race"),
	}

	fields := requestFinishedFields(&line)

	require.Nil(t, fieldValue(fields, logkey.NonceFinished))
}

// Test flow:
//  1. Table-driven: each case builds a `RaceOutcome` and expects an offset, a round trip, and whether a stamp was found — covering an agreeing clock, a drifted clock, a slow round trip not charged as drift, the loser's stamp being ignored, and a reply with no receipt or completion-only stamp.
//  2. For each case, call `hostClockOffset`.
//  3. Assert the returned offset, round trip, and found flag match the case's expectation.
func TestHostClockOffsetReadsTheWinnersStamp(t *testing.T) {
	t.Parallel()
	dispatchedAt := time.Unix(1786114580, 0)
	confirmedAt := dispatchedAt.Add(120 * time.Millisecond)
	testCases := []struct {
		name         string
		outcome      engine.RaceOutcome
		wantOffsetMS int64
		wantRoundTri int64
		wantFound    bool
	}{
		{
			name: "a host whose clock agrees with ours",
			outcome: engine.RaceOutcome{WinnerNonce: 7, Attempts: []engine.AttemptOutcome{
				{Nonce: 7, SendTime: dispatchedAt, ReceiptTime: confirmedAt, ConfirmedAt: 1786114584},
			}},
			wantOffsetMS: 4440,
			wantRoundTri: 120,
			wantFound:    true,
		},
		{
			name: "a host stamping before we dispatched, which only a drifted clock can do",
			outcome: engine.RaceOutcome{WinnerNonce: 7, Attempts: []engine.AttemptOutcome{
				{Nonce: 7, SendTime: dispatchedAt, ReceiptTime: confirmedAt, ConfirmedAt: 1786114550},
			}},
			wantOffsetMS: -29560,
			wantRoundTri: 120,
			wantFound:    true,
		},
		{
			name: "a slow round trip is not charged to the host as drift",
			outcome: engine.RaceOutcome{WinnerNonce: 7, Attempts: []engine.AttemptOutcome{
				{
					Nonce: 7, SendTime: dispatchedAt,
					ReceiptTime: dispatchedAt.Add(4 * time.Second), ConfirmedAt: 1786114584,
				},
			}},
			wantOffsetMS: 2500,
			wantRoundTri: 4000,
			wantFound:    true,
		},
		{
			name: "the loser's stamp is not the winner's",
			outcome: engine.RaceOutcome{WinnerNonce: 7, Attempts: []engine.AttemptOutcome{
				{Nonce: 9, SendTime: dispatchedAt, ReceiptTime: confirmedAt, ConfirmedAt: 1786114999},
				{Nonce: 7, SendTime: dispatchedAt, ReceiptTime: confirmedAt, ConfirmedAt: 1786114584},
			}},
			wantOffsetMS: 4440,
			wantRoundTri: 120,
			wantFound:    true,
		},
		{
			name: "a completion stamp is not a receipt stamp",
			outcome: engine.RaceOutcome{WinnerNonce: 7, Attempts: []engine.AttemptOutcome{
				{Nonce: 7, SendTime: dispatchedAt, ReceiptTime: confirmedAt},
			}},
		},
		{
			name: "a reply that never carried a receipt reports nothing rather than an epoch offset",
			outcome: engine.RaceOutcome{WinnerNonce: 7, Attempts: []engine.AttemptOutcome{
				{Nonce: 7, SendTime: dispatchedAt, ReceiptTime: confirmedAt},
			}},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			offsetMS, roundTripMS, found := hostClockOffset(testCase.outcome)

			if found != testCase.wantFound {
				t.Fatalf("found = %v, want %v", found, testCase.wantFound)
			}
			if offsetMS != testCase.wantOffsetMS {
				t.Errorf("offsetMS = %d, want %d", offsetMS, testCase.wantOffsetMS)
			}
			if roundTripMS != testCase.wantRoundTri {
				t.Errorf("roundTripMS = %d, want %d", roundTripMS, testCase.wantRoundTri)
			}
		})
	}
}

// Test flow:
//  1. Build a `RaceOutcome` whose dispatch time carries sub-second precision, one the executor's whole-second stamp truncates.
//  2. Call `hostClockOffset`.
//  3. Assert a stamp was found and the offset stays within the truncated second rather than reading as host clock drift.
func TestHostClockOffsetDoesNotReadTruncationAsDrift(t *testing.T) {
	t.Parallel()
	dispatchedAt := time.Unix(1786114580, 0).Add(900 * time.Millisecond)

	offsetMS, _, stamped := hostClockOffset(engine.RaceOutcome{
		WinnerNonce: 7,
		Attempts: []engine.AttemptOutcome{{
			Nonce: 7, SendTime: dispatchedAt,
			ReceiptTime: dispatchedAt.Add(200 * time.Millisecond),
			ConfirmedAt: 1786114580,
		}},
	})

	if !stamped {
		t.Fatal("a signed receipt must be readable")
	}
	if offsetMS < -600 || offsetMS > 600 {
		t.Errorf("offsetMS = %d, want a synchronised host inside the truncated second", offsetMS)
	}
}

// Test flow:
//  1. Build a `HostApplicationError` whose raw payload is a 100,000-byte string.
//  2. Render it via `loggedError`.
//  3. Assert the result stays bounded near `maxLoggedErrorBytes` and ends with the truncation marker.
func TestLoggedErrorBoundsHostControlledText(t *testing.T) {
	huge := &engine.HostApplicationError{Payload: strings.Repeat("A", 100_000)}

	logged := loggedError(huge)

	if len(logged) > maxLoggedErrorBytes+len("…(truncated)") {
		t.Fatalf("logged error is %d bytes, want it bounded near %d", len(logged), maxLoggedErrorBytes)
	}
	if !strings.HasSuffix(logged, "…(truncated)") {
		t.Fatalf("logged = %q, want it to say it was cut", logged)
	}
}

// Test flow:
//  1. Build a plain error with a short message.
//  2. Render it via `loggedError`.
//  3. Assert the message comes back unchanged.
func TestLoggedErrorLeavesAShortErrorAlone(t *testing.T) {
	if got := loggedError(errors.New("no host available")); got != "no host available" {
		t.Fatalf("loggedError() = %q, want the error unchanged", got)
	}
}

// Test flow:
//  1. Build a `HostApplicationError` whose payload is 1000 multi-byte runes.
//  2. Render it via `loggedError`.
//  3. Assert the truncated result is still valid UTF-8.
func TestLoggedErrorCutsOnARuneBoundary(t *testing.T) {
	if maxLoggedErrorBytes%3 == 0 {
		t.Fatalf("maxLoggedErrorBytes = %d divides by the test rune width, so this asserts nothing", maxLoggedErrorBytes)
	}
	logged := loggedError(&engine.HostApplicationError{Payload: strings.Repeat("日", 1000)})

	if !utf8.ValidString(logged) {
		t.Fatalf("logged error is not valid UTF-8: %q", logged)
	}
}

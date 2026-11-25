package engine

import (
	"bytes"
	"testing"

	"devshard/gatewayprotocol/streamevidence"
	"devshard/internal/testutil"
)

// Test flow:
//  1. Build a terminal-error stream with `testutil.LongErrorStream` whose content lines run past 64 KiB, written here as a literal.
//  2. Classify the stream one event at a time with an attempt's SSE classifier, as the engine builds it, and flush it.
//  3. Assert the retained lines, each counted with one separator, fit in 64 KiB and that the stream's next line would not.
func TestAnAttemptRetainsSixtyFourKibibytesOfErrorStream(t *testing.T) {
	const sixtyFourKibibytes = 64 * 1024
	fixture := testutil.LongErrorStream(sixtyFourKibibytes)
	lines := streamevidence.DataLines(fixture)
	classifier := newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), testParticipant, false, nil)

	for _, event := range bytes.SplitAfter(fixture, []byte("\n\n")) {
		classifier.Classify(event)
	}
	classifier.Flush()

	retained := classifier.retained.lines
	if len(retained) >= len(lines) {
		t.Fatalf("retained all %d lines, want the bound to cut a stream that runs past 64 KiB", len(lines))
	}
	retainedBytes := 0
	for _, line := range retained {
		retainedBytes += len(line) + 1
	}
	if retainedBytes > sixtyFourKibibytes {
		t.Errorf("retained %d bytes, want at most %d", retainedBytes, sixtyFourKibibytes)
	}
	if nextLine := lines[len(retained)]; retainedBytes+len(nextLine)+1 <= sixtyFourKibibytes {
		t.Errorf("retained %d bytes and stopped, though the next line of %d bytes still fits in %d", retainedBytes, len(nextLine), sixtyFourKibibytes)
	}
}

package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/gatewayprotocol/streamevidence"
)

const errorStreamFixtures = "../../../gatewayprotocol/streamevidence/testdata"

// Test flow:
//  1. For each error stream fixture in `gatewayprotocol/streamevidence/testdata`, ask `streamevidence.ProvesErrorMiss` whether the payload of the leading evidence lines that fit in `streamevidence.MaxRetainedBytes` proves the error.
//  2. Run an attempt whose host streams the fixture through the real `sseClassifier`, once as one read per SSE event and once as two reads split inside its error event, while a goroutine collects the attempt's events so a long fixture cannot fill the channel.
//  3. Plan the race's timeout steps from the attempt's outcome.
//  4. Assert the one step settles by a miss claim exactly when the shared rule proves it over those retained lines.
func TestAnErrorStreamIsClaimedExactlyWhenTheSharedEvidenceRuleProvesIt(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(errorStreamFixtures, "*.sse"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)

	for _, path := range paths {
		stream, err := os.ReadFile(path)
		require.NoError(t, err)
		payload, err := streamevidence.Payload(retainedLines(streamevidence.DataLines(stream)))
		require.NoError(t, err)
		wantClaim := streamevidence.ProvesErrorMiss(payload)

		for _, delivery := range []struct {
			name  string
			reads []string
		}{
			{name: "one_read_per_event", reads: readsOnePerEvent(stream)},
			{name: "error_event_split_across_reads", reads: readsSplitInsideTheErrorEvent(t, stream)},
		} {
			t.Run(filepath.Base(path)+"/"+delivery.name, func(t *testing.T) {
				dispatch := &fakeDispatcher{receipt: true, chunks: delivery.reads, response: fakeResponse{confirmed: true}}
				fixture := newAttemptFixture(dispatch, &fakeClassifier{})
				fixture.spec.Classifier = newTestClassifier(false, nil)

				outcome := *doneEvent(t, runAttemptCollectingEvents(t, fixture)).Outcome
				steps := RaceOutcome{Attempts: []AttemptOutcome{outcome}}.TimeoutPlan()

				require.Len(t, steps, 1)
				require.Equal(t, wantClaim, steps[0].Kind == SettleByMissClaim)
			})
		}
	}
}

func readsSplitInsideTheErrorEvent(t *testing.T, stream []byte) []string {
	t.Helper()
	split := bytes.Index(stream, []byte(`"error"`))
	require.Positive(t, split, "every error stream fixture names an error")
	return []string{string(stream[:split]), string(stream[split:])}
}

func readsOnePerEvent(stream []byte) []string {
	var reads []string
	for _, event := range bytes.SplitAfter(stream, []byte("\n\n")) {
		if len(event) > 0 {
			reads = append(reads, string(event))
		}
	}
	return reads
}

func retainedLines(lines []string) []string {
	var kept []string
	heldBytes := 0
	for _, line := range lines {
		heldBytes += len(line) + 1
		if heldBytes > streamevidence.MaxRetainedBytes {
			break
		}
		kept = append(kept, line)
	}
	return kept
}

func runAttemptCollectingEvents(t *testing.T, fixture *attemptFixture) []AttemptEvent {
	t.Helper()
	var collected []AttemptEvent
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for event := range fixture.events {
			collected = append(collected, event)
		}
	}()

	runAttempt(context.Background(), fixture.spec)
	close(fixture.events)

	select {
	case <-collectorDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the event collector did not finish within 10s after the attempt closed its events channel")
	}
	return collected
}

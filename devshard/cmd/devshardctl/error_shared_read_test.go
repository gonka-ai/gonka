package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/gatewayprotocol/streamevidence"
)

// Test flow:
//  1. Read `content_then_error.sse` into its role, content, error and `[DONE]` events.
//  2. Deliver those events in reads that put the error alone, after content in one read, before content in one read, and split mid-event with content sharing the read of either half.
//  3. Assert the shared rule `streamevidence.ProvesErrorMiss`, on which the new gateway claims, proves the error for the evidence lines of the bytes delivered.
//  4. Write the reads through a fresh `raceWriter`, flush the final event, end the send and give the attempt a Finish.
//  5. Assert every split ends as an error stream attempt whose miss is claimable and whose client-facing error is the error event.
func TestAnErrorSharingAReadWithContentEndsTheAttemptAsAnErrorStream(t *testing.T) {
	stream, err := os.ReadFile(filepath.Join(errorStreamFixtures, "content_then_error.sse"))
	require.NoError(t, err)
	events := readsOnePerEvent(stream)
	require.Len(t, events, 4)
	role, content, errorEvent, done := events[0], events[1], events[2], events[3]
	errorSplit := bytes.Index(errorEvent, []byte(`"error"`))
	require.Positive(t, errorSplit)
	errorPayload := bytes.TrimSpace(bytes.TrimPrefix(errorEvent, []byte("data:")))

	for _, delivery := range []struct {
		name  string
		reads [][]byte
	}{
		{name: "error_alone_in_its_read", reads: [][]byte{role, content, errorEvent, done}},
		{name: "error_after_content_in_one_read", reads: [][]byte{role, bytes.Join([][]byte{content, errorEvent}, nil), done}},
		{name: "error_before_content_in_one_read", reads: [][]byte{role, bytes.Join([][]byte{errorEvent, content}, nil), done}},
		{name: "error_split_mid_event_after_content", reads: [][]byte{role, bytes.Join([][]byte{content, errorEvent[:errorSplit]}, nil), bytes.Join([][]byte{errorEvent[errorSplit:], done}, nil)}},
		{name: "error_split_mid_event_before_content", reads: [][]byte{role, errorEvent[:errorSplit], bytes.Join([][]byte{errorEvent[errorSplit:], content}, nil), done}},
	} {
		t.Run(delivery.name, func(t *testing.T) {
			payload, err := streamevidence.Payload(streamevidence.DataLines(bytes.Join(delivery.reads, nil)))
			require.NoError(t, err)
			require.True(t, streamevidence.ProvesErrorMiss(payload), "shared rule proves the error")

			attempt := streamAttemptToItsFinish(t, delivery.reads)

			require.True(t, isErrorStreamAttempt(attempt), "error stream attempt")
			_, claimable := errorMissOutcome(attempt, nil)
			require.True(t, claimable, "error miss claimable")
			clientError := hostApplicationErrorFromInflight(attempt)
			require.NotNil(t, clientError, "client-facing error")
			require.JSONEq(t, string(errorPayload), string(clientError.jsonPayload()))
		})
	}
}

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/gatewayprotocol/streamevidence"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

const errorStreamFixtures = "../../gatewayprotocol/streamevidence/testdata"

// Test flow:
//  1. For each error stream fixture in `gatewayprotocol/streamevidence/testdata`, ask `streamevidence.ProvesErrorMiss` whether the payload of the leading evidence lines that fit in `streamevidence.MaxRetainedBytes` proves the error.
//  2. Write the fixture through a fresh `raceWriter` once as one read per SSE event and once as two reads split inside its error event, then flush the final event.
//  3. End the send and give the attempt a Finish in its response mempool.
//  4. Assert the `claimable` result of `errorMissOutcome` is true exactly when the shared rule proves it over those retained lines.
func TestErrorMissClaimableFollowsTheSharedEvidenceRule(t *testing.T) {
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
			reads [][]byte
		}{
			{name: "one_read_per_event", reads: readsOnePerEvent(stream)},
			{name: "error_event_split_across_reads", reads: readsSplitInsideTheErrorEvent(t, stream)},
		} {
			t.Run(filepath.Base(path)+"/"+delivery.name, func(t *testing.T) {
				attempt := streamAttemptToItsFinish(t, delivery.reads)

				_, claimable := errorMissOutcome(attempt, nil)
				require.Equal(t, wantClaim, claimable)
			})
		}
	}
}

// Test flow:
//  1. Build a finished attempt that saw a terminal error and holds a Finish in its response mempool but retained no evidence lines.
//  2. Assert `errorMissOutcome` still reports `hasFinish` but not `claimable`, since its payload is empty.
func TestErrorMissClaimableRefusesAFinishWithoutEvidence(t *testing.T) {
	const nonce = 7
	inf := &inflight{
		nonce:         nonce,
		errorSource:   "error.InternalServerError",
		errorTerminal: true,
		resp: &host.HostResponse{Mempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: nonce}}},
		}},
	}

	hasFinish, claimable := errorMissOutcome(inf, nil)
	require.True(t, hasFinish)
	require.False(t, claimable)
}

func streamAttemptToItsFinish(t *testing.T, reads [][]byte) *inflight {
	t.Helper()
	ctx := context.Background()
	var sink bytes.Buffer
	const nonce = 7
	writer, attempt := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), nonce)
	for _, read := range reads {
		_, err := writer.Write(read)
		require.NoError(t, err)
	}
	writer.flushClassify()
	close(attempt.done)
	attempt.resp = &host.HostResponse{Mempool: []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: nonce}}},
	}}
	return attempt
}

func readsSplitInsideTheErrorEvent(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	split := bytes.Index(stream, []byte(`"error"`))
	require.Positive(t, split, "every error stream fixture names an error")
	return [][]byte{stream[:split], stream[split:]}
}

func readsOnePerEvent(stream []byte) [][]byte {
	var reads [][]byte
	for _, event := range bytes.SplitAfter(stream, []byte("\n\n")) {
		if len(event) > 0 {
			reads = append(reads, event)
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

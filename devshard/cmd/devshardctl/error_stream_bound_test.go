package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/gatewayprotocol/streamevidence"
	"devshard/internal/testutil"
)

// Test flow:
//  1. Build a terminal-error stream with `testutil.LongErrorStream` whose content lines run past 1 MiB, written here as a literal.
//  2. Write the stream one event at a time through a race writer of an error attempt, with the evidence cap left at its default, and flush it.
//  3. Assert the retained lines, each counted with one separator, fit in 1 MiB and that the stream's next line would not.
func TestRaceWriterRetainsOneMebibyteOfErrorStreamByDefault(t *testing.T) {
	const oneMebibyte = 1 << 20
	fixture := testutil.LongErrorStream(oneMebibyte)
	lines := streamevidence.DataLines(fixture)
	ctx := context.Background()
	var sink bytes.Buffer
	rw, inf := newErrorRaceWriter(t, newRaceGroup(ctx, ctx, "escrow-x", &sink), 1)

	for _, event := range bytes.SplitAfter(fixture, []byte("\n\n")) {
		_, err := rw.Write(event)
		require.NoError(t, err)
	}
	rw.flushClassify()

	require.Less(t, len(inf.errorStreamLines), len(lines), "the default cap must cut a stream that runs past 1 MiB")
	retainedBytes := 0
	for _, line := range inf.errorStreamLines {
		retainedBytes += len(line) + 1
	}
	nextLine := lines[len(inf.errorStreamLines)]
	require.LessOrEqual(t, retainedBytes, oneMebibyte)
	require.Greater(t, retainedBytes+len(nextLine)+1, oneMebibyte)
}

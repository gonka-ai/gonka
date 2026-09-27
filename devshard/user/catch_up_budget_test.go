package user

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
	"devshard/types"
)

func diffOfSize(nonce uint64, promptHashBytes int) types.Diff {
	return types.Diff{
		Nonce: nonce,
		Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: nonce,
			PromptHash:  make([]byte, promptHashBytes),
		}}}},
		UserSig:       make([]byte, 65),
		PostStateRoot: make([]byte, 32),
	}
}

func diffsOfSize(count int, promptHashBytes int) []types.Diff {
	diffs := make([]types.Diff, count)
	for i := range diffs {
		diffs[i] = diffOfSize(uint64(i+1), promptHashBytes)
	}
	return diffs
}

func TestTheChunkStopsAtTheByteBudget(t *testing.T) {
	diffs := diffsOfSize(10, 1024)
	budgetBytes := transport.EncodedDiffSize(diffs[0]) * 3

	require.Equal(t, 3, countDiffsThatFit(diffs, budgetBytes))
	require.Equal(t, 3, countDiffsThatFit(diffs, budgetBytes+transport.EncodedDiffSize(diffs[0])-1),
		"a diff that only partly fits does not travel")
	require.Equal(t, 4, countDiffsThatFit(diffs, budgetBytes+transport.EncodedDiffSize(diffs[0])),
		"a diff that exactly fits does travel")
}

func TestTheChunkKeepsTheCountCap(t *testing.T) {
	diffs := diffsOfSize(catchUpChunkSize+50, 8)

	require.Equal(t, catchUpChunkSize, countDiffsThatFit(diffs, transport.CatchUpBudgetBytes),
		"the count cap bounds the per-request work even when the bytes fit")
}

func TestAnOversizedDiffFitsNowhere(t *testing.T) {
	diffs := []types.Diff{diffOfSize(1, 4096), diffOfSize(2, 8)}
	budgetBytes := transport.EncodedDiffSize(diffs[1])

	require.Zero(t, countDiffsThatFit(diffs, budgetBytes),
		"a diff no body can carry is refused, not shipped into a certain 413")
	require.Equal(t, 1, countDiffsThatFit(diffs[1:], budgetBytes), "the small one fits on its own")
}

func TestNoDiffsFitNothing(t *testing.T) {
	require.Zero(t, countDiffsThatFit(nil, transport.CatchUpBudgetBytes))
}

func TestOneBodyCountsTheReserveToo(t *testing.T) {
	diffs := diffsOfSize(3, 1024)
	diffBytes := transport.EncodedDiffSize(diffs[0]) * 3

	require.True(t, catchUpFitsOneBody(diffs, 0, diffBytes))
	require.True(t, catchUpFitsOneBody(diffs, 10, diffBytes+10))
	require.False(t, catchUpFitsOneBody(diffs, 11, diffBytes+10),
		"the prompt's own bytes have to be charged against the same budget")
}

func TestOneBodyRefusesAReserveOverTheBudget(t *testing.T) {
	require.False(t, catchUpFitsOneBody(nil, 101, 100),
		"a host that needs no diffs still cannot read a prompt over the cap")
	require.False(t, catchUpFitsOneBody(nil, 100, 100),
		"a reserve filling the whole budget leaves no room for the diff that must ride with it")
	require.True(t, catchUpFitsOneBody(nil, 99, 100))
}

func TestOneBodyRefusesMoreDiffsThanTheCountCap(t *testing.T) {
	diffs := diffsOfSize(catchUpChunkSize+1, 8)

	require.False(t, catchUpFitsOneBody(diffs, 0, transport.CatchUpBudgetBytes),
		"the count cap holds even when every diff is tiny")
}

func TestTheChunkRefusesADiffThatFitsNowhere(t *testing.T) {
	diffs := []types.Diff{diffOfSize(1, 4096), diffOfSize(2, 8)}

	_, err := catchUpChunk(diffs, transport.EncodedDiffSize(diffs[1]))

	require.ErrorIs(t, err, ErrTailTooLargeForHost)
	require.ErrorIs(t, err, ErrRequestTooLargeForHost)
}

func TestTheChunkStopsAtThePrefixThatFits(t *testing.T) {
	diffs := diffsOfSize(10, 1024)
	budgetBytes := transport.EncodedDiffSize(diffs[0]) * 3

	chunk, err := catchUpChunk(diffs, budgetBytes)

	require.NoError(t, err)
	require.Len(t, chunk, 3)
	require.Equal(t, diffs[0].Nonce, chunk[0].Nonce)
	require.Equal(t, diffs[2].Nonce, chunk[len(chunk)-1].Nonce)
}

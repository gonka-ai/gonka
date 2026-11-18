package transport_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/types"
)

func coldStartTx(version string) []*types.DevshardTx {
	return []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, ProtocolVersion: version,
	}}}}
}

func coldRefusal(t *testing.T, srv *transport.Server) (*transport.VerifyTimeoutResponse, *recordingExecutor) {
	t.Helper()
	exec := &recordingExecutor{t: t, headErr: errors.New("session not found")}
	srv.SetPeerClients(map[int]transport.HostPeerClient{1: exec})
	resp, err := srv.ServeVerifyTimeout(context.Background(), refusalPayloadRequest())
	require.NoError(t, err)
	return resp, exec
}

func TestRefusalColdExecutorFirstPageWithoutStartRejects(t *testing.T) {
	srv, _ := newRefusalJournalServerWith(t, 3, func(int) []*types.DevshardTx { return nil })
	resp, exec := coldRefusal(t, srv)
	require.False(t, resp.Accept)
	require.Empty(t, exec.calls)
}

func TestRefusalColdExecutorUnversionedStartRejects(t *testing.T) {
	srv, _ := newRefusalJournalServerWith(t, 3, func(nonce int) []*types.DevshardTx {
		if nonce == 1 {
			return coldStartTx("")
		}
		return nil
	})
	resp, exec := coldRefusal(t, srv)
	require.False(t, resp.Accept)
	require.Empty(t, exec.calls)
}

func TestRefusalColdExecutorStartPastFirstPageRejects(t *testing.T) {
	last := host.ChallengePageMaxNonces + 1
	srv, _ := newRefusalJournalServerWith(t, last, func(nonce int) []*types.DevshardTx {
		if nonce == last {
			return coldStartTx(testutil.RuntimeTestVersion)
		}
		return nil
	})
	resp, exec := coldRefusal(t, srv)
	require.False(t, resp.Accept)
	require.Empty(t, exec.calls)
}

func TestRefusalColdExecutorVersionedStartInFirstPageStillChallenges(t *testing.T) {
	srv, _ := newRefusalJournalServerWith(t, 3, func(nonce int) []*types.DevshardTx {
		if nonce == 1 {
			return coldStartTx(testutil.RuntimeTestVersion)
		}
		return nil
	})
	resp, exec := coldRefusal(t, srv)
	require.True(t, resp.Accept)
	require.NotEmpty(t, exec.calls)
}

func TestRefusalColdClaimFromAnExecutorThatConfirmedEarlierIsChallenged(t *testing.T) {
	hash := testutil.TestPromptHash
	confirmed := &types.InferenceRecord{
		Status: types.StatusTimedOut, ExecutorSlot: 1, ReservedCost: 150, PromptHash: hash[:], Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, ConfirmedAtHeight: 5,
	}
	srv, _ := newRefusalJournalServerFull(t, 3, func(int) []*types.DevshardTx { return nil }, map[uint64]*types.InferenceRecord{17: confirmed})
	resp, exec := coldRefusal(t, srv)
	require.True(t, resp.Accept)
	require.NotEmpty(t, exec.calls)
}

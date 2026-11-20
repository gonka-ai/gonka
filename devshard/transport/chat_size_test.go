package transport

import (
	"testing"

	"devshard/host"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestChatDiffPrefixAccountsForBase64AndPrompt(t *testing.T) {
	makeDiff := func(nonce uint64, bytes int) types.Diff {
		return types.Diff{Nonce: nonce, Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_MainnetHeight{
			MainnetHeight: &types.MsgMainnetHeight{Height: 1, LightBlocks: [][]byte{make([]byte, bytes)}},
		}}}}
	}

	for _, tc := range []struct {
		name string
		req  host.HostRequest
		fit  int
	}{
		{"large single proof", host.HostRequest{Nonce: 1, Diffs: []types.Diff{makeDiff(1, 8<<20)}}, 0},
		{"several individually sendable diffs", host.HostRequest{Nonce: 2, Diffs: []types.Diff{makeDiff(1, 4<<20), makeDiff(2, 4<<20)}}, 1},
		{"prompt plus proof", host.HostRequest{Nonce: 1, Diffs: []types.Diff{makeDiff(1, 4<<20)},
			Payload: &host.InferencePayload{Prompt: make([]byte, 5<<20)}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fit, err := ChatDiffPrefix(tc.req, "42")
			require.NoError(t, err)
			require.Equal(t, tc.fit, fit)
			ir, err := HostRequestToJSON(tc.req)
			require.NoError(t, err)
			body, _, err := marshalChatBody(nil, ir)
			require.NoError(t, err)
			require.Greater(t, int64(len(body)), DefaultMaxBodySize)
			if fit > 0 {
				ir.Diffs = ir.Diffs[:fit]
				body, _, err = marshalChatBody(nil, ir)
				require.NoError(t, err)
				require.Less(t, int64(len(body)), DefaultMaxBodySize)
			}
		})
	}
}

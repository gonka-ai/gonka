// msg_server_submit_seed_test.go
package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestSubmitSeed(t *testing.T) {
	tests := []struct {
		name                string
		effectiveEpochIndex int64
		inputMsg            *types.MsgSubmitSeed
		expectErr           error
		expectCalled        bool
	}{
		{
			name:                "successful submission for current epoch",
			effectiveEpochIndex: 10,
			inputMsg: &types.MsgSubmitSeed{
				Creator:    testutil.Executor,
				EpochIndex: 10,
				Signature:  "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			},
			expectErr:    nil,
			expectCalled: true,
		},
		{
			name:                "successful submission for upcoming epoch",
			effectiveEpochIndex: 10,
			inputMsg: &types.MsgSubmitSeed{
				Creator:    testutil.Creator,
				EpochIndex: 11,
				Signature:  "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			},
			expectErr:    nil,
			expectCalled: true,
		},
		{
			name:                "unsuccessful submission for previous epoch",
			effectiveEpochIndex: 10,
			inputMsg: &types.MsgSubmitSeed{
				Creator:    testutil.Creator,
				EpochIndex: 9,
				Signature:  "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			},
			expectErr:    types.ErrEpochIndexOutOfRange,
			expectCalled: false,
		},
		{
			name:                "unsuccessful submission for 2 epochs ahead",
			effectiveEpochIndex: 10,
			inputMsg: &types.MsgSubmitSeed{
				Creator:    testutil.Creator,
				EpochIndex: 12,
				Signature:  "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			},
			expectErr:    types.ErrEpochIndexOutOfRange,
			expectCalled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, ms, ctx, _ := setupKeeperWithMocks(t)
			if tc.effectiveEpochIndex > 0 {
				k.SetEffectiveEpochIndex(ctx, uint64(tc.effectiveEpochIndex))
			}
			k.SetEpoch(ctx, &types.Epoch{Index: uint64(tc.effectiveEpochIndex + 1)})
			// Call the function
			k.SetParticipant(ctx, types.Participant{
				Index: tc.inputMsg.Creator,
			})
			resp, err := ms.SubmitSeed(ctx, tc.inputMsg)

			// Assertions
			if tc.expectErr != nil {
				require.ErrorIs(t, err, tc.expectErr)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
			}
		})
	}
}

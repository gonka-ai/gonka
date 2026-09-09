package tx_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	abcitypes "github.com/cometbft/cometbft/abci/types"

	chaintx "common/chain/tx"
	"github.com/stretchr/testify/require"
)

func TestCreatedEscrowIDFromTxResponse(t *testing.T) {
	resp := &sdk.TxResponse{
		Events: []abcitypes.Event{{
			Type: "devshard_escrow_created",
			Attributes: []abcitypes.EventAttribute{
				{Key: "escrow_id", Value: "42"},
			},
		}},
	}
	id, ok := chaintx.CreatedEscrowIDFromTxResponse(resp)
	require.True(t, ok)
	require.Equal(t, uint64(42), id)
}

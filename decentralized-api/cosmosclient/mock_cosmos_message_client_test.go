package cosmosclient

import (
	"testing"

	"decentralized-api/cosmosclient/tx_manager"

	sdk "github.com/cosmos/cosmos-sdk/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMockCosmosMessageClient_ForwardsTxSendOptions(t *testing.T) {
	msg := &inferencetypes.MsgPoCV2StoreCommit{Creator: "alice"}
	opts := tx_manager.TxSendOptions{TimeoutHeight: 250}

	noRetry := &MockCosmosMessageClient{}
	noRetry.On("SendTransactionAsyncNoRetry", msg, opts).Return((*sdk.TxResponse)(nil), nil)
	_, err := noRetry.SendTransactionAsyncNoRetry(msg, opts)
	require.NoError(t, err)
	noRetry.AssertExpectations(t)

	withRetry := &MockCosmosMessageClient{}
	withRetry.On("SendTransactionAsyncWithRetry", msg, opts).Return(&sdk.TxResponse{TxHash: "abc"}, nil)
	resp, err := withRetry.SendTransactionAsyncWithRetry(msg, opts)
	require.NoError(t, err)
	require.Equal(t, "abc", resp.TxHash)
	withRetry.AssertExpectations(t)
}

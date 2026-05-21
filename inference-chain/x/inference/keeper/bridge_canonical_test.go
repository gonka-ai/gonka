package keeper_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestBuildCanonicalBridgeTransaction_KeyCollapsesLeadingZerosAndReceiptsRootCasing(t *testing.T) {
	owner := "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	receiptsLower := "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	receiptsMixed := "0xAbCdEf1234567890aBcDeF1234567890aBcDeF1234567890aBcDeF1234567890"

	msgA := &types.MsgBridgeExchange{
		Validator:       owner,
		OriginChain:     "Ethereum",
		ContractAddress: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
		OwnerAddress:    owner,
		OwnerPubKey:     "pk",
		Amount:          "01000",
		BlockNumber:     "021000000",
		ReceiptIndex:    "05",
		ReceiptsRoot:    receiptsMixed,
	}
	msgB := &types.MsgBridgeExchange{
		Validator:       owner,
		OriginChain:     "ethereum",
		ContractAddress: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		OwnerAddress:    owner,
		OwnerPubKey:     "pk",
		Amount:          "1000",
		BlockNumber:     "21000000",
		ReceiptIndex:    "5",
		ReceiptsRoot:    receiptsLower,
	}

	txA, err := keeper.BuildCanonicalBridgeTransactionForTest(msgA)
	require.NoError(t, err)
	txB, err := keeper.BuildCanonicalBridgeTransactionForTest(msgB)
	require.NoError(t, err)

	require.Equal(t, txA, txB)
	require.Equal(t, keeper.GenerateSecureBridgeTransactionKeyForTest(txA), keeper.GenerateSecureBridgeTransactionKeyForTest(txB))
	require.Equal(t, keeper.CanonicalBridgeDepositEventIDForTest(txA), keeper.CanonicalBridgeDepositEventIDForTest(txB))
}

func TestCanonicalBridgeDepositEventID_CollapsesLegacyNumericVariants(t *testing.T) {
	owner := "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	base := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		OwnerAddress:    owner,
		Amount:          "1000",
		BlockNumber:     "21000000",
		ReceiptIndex:    "5",
		ReceiptsRoot:    "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
	}
	legacyVariant := &types.BridgeTransaction{
		ChainId:         "Ethereum",
		ContractAddress: "0xdAC17F958D2ee523a2206206994597C13D831ec7",
		OwnerAddress:    owner,
		Amount:          "01000",
		BlockNumber:     "021000000",
		ReceiptIndex:    "05",
		ReceiptsRoot:    "0xABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890",
	}

	require.Equal(t, keeper.CanonicalBridgeDepositEventIDForTest(base), keeper.CanonicalBridgeDepositEventIDForTest(legacyVariant))
}

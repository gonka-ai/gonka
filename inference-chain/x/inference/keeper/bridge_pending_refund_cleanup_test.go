package keeper_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestCleanupBridgePendingRefundByBlsRequestID_RemovesMatchingEntries(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)

	mintID := bytes.Repeat([]byte{0x11}, 32)
	withdrawID := bytes.Repeat([]byte{0x12}, 32)
	otherMintID := bytes.Repeat([]byte{0x13}, 32)

	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, hex.EncodeToString(mintID), types.MsgRequestBridgeMint{
		Creator:            "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		Amount:             "1000",
		DestinationAddress: "0xabc",
		ChainId:            "ethereum",
	}))
	require.NoError(t, k.BridgeMintRefundsMap.Set(ctx, hex.EncodeToString(otherMintID), types.MsgRequestBridgeMint{
		Creator:            "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		Amount:             "2000",
		DestinationAddress: "0xdef",
		ChainId:            "ethereum",
	}))
	require.NoError(t, k.BridgeWithdrawalRefundsMap.Set(ctx, hex.EncodeToString(withdrawID), types.MsgRequestBridgeWithdrawal{
		Creator:            "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		UserAddress:        "gonka1rdyphrqxe9l5hkp7uxcruch64sh337jasqsntr",
		Amount:             "3000",
		DestinationAddress: "0x123",
	}))
	require.NoError(t, k.BridgeWithdrawalTokenRefsMap.Set(ctx, hex.EncodeToString(withdrawID), types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0x123",
	}))

	require.NoError(t, k.CleanupBridgePendingRefundByBlsRequestID(ctx, mintID))
	require.NoError(t, k.CleanupBridgePendingRefundByBlsRequestID(ctx, withdrawID))
	require.NoError(t, k.CleanupBridgePendingRefundByBlsRequestID(ctx, bytes.Repeat([]byte{0x77}, 32)))

	_, err := k.BridgeMintRefundsMap.Get(ctx, hex.EncodeToString(mintID))
	require.ErrorIs(t, err, collections.ErrNotFound)

	_, err = k.BridgeWithdrawalRefundsMap.Get(ctx, hex.EncodeToString(withdrawID))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.BridgeWithdrawalTokenRefsMap.Get(ctx, hex.EncodeToString(withdrawID))
	require.ErrorIs(t, err, collections.ErrNotFound)

	stillPending, err := k.BridgeMintRefundsMap.Get(ctx, hex.EncodeToString(otherMintID))
	require.NoError(t, err)
	require.Equal(t, "2000", stillPending.Amount)
}

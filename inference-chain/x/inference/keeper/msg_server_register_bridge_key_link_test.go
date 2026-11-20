package keeper_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	dcrec "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/sha3"
)

func TestMsgServer_RegisterBridgeKeyLink(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	gonkaPriv := secp256k1.GenPrivKey()
	ethPriv := secp256k1.GenPrivKey()
	creator := sdk.AccAddress(gonkaPriv.PubKey().Address())
	account := authtypes.NewBaseAccount(creator, gonkaPriv.PubKey(), 1, 0)

	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creator).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), creator).Return(account).AnyTimes()

	msg := bridgeKeyLinkMsg(t, ctx.ChainID(), gonkaPriv, ethPriv, 1)
	resp, err := ms.RegisterBridgeKeyLink(ctx, msg)
	require.NoError(t, err)

	ethDerived := sdk.AccAddress(ethPriv.PubKey().Address()).String()
	require.Equal(t, ethDerived, resp.OwnerAddress)
	require.Equal(t, creator.String(), resp.RecipientAddress)
	require.Equal(t, uint64(1), resp.Nonce)

	linked, err := k.ResolveBridgeRecipient(ctx, ethDerived)
	require.NoError(t, err)
	require.Equal(t, creator.String(), linked)

	unlinked, err := k.ResolveBridgeRecipient(ctx, creator.String())
	require.NoError(t, err)
	require.Equal(t, creator.String(), unlinked)
}

func TestMsgServer_RegisterBridgeKeyLink_RejectsBadSignature(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	gonkaPriv := secp256k1.GenPrivKey()
	ethPriv := secp256k1.GenPrivKey()
	creator := sdk.AccAddress(gonkaPriv.PubKey().Address())
	account := authtypes.NewBaseAccount(creator, gonkaPriv.PubKey(), 1, 0)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creator).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), creator).Return(account).AnyTimes()

	msg := bridgeKeyLinkMsg(t, ctx.ChainID(), gonkaPriv, ethPriv, 1)
	msg.EthSignature[0] ^= 0xff
	_, err := ms.RegisterBridgeKeyLink(ctx, msg)
	require.ErrorIs(t, err, types.ErrInvalidBridgeKeyLink)
}

func TestMsgServer_RegisterBridgeKeyLink_RejectsAccountKeyMismatch(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	gonkaPriv := secp256k1.GenPrivKey()
	otherPriv := secp256k1.GenPrivKey()
	ethPriv := secp256k1.GenPrivKey()
	creator := sdk.AccAddress(gonkaPriv.PubKey().Address())
	account := authtypes.NewBaseAccount(creator, otherPriv.PubKey(), 1, 0)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creator).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), creator).Return(account).AnyTimes()

	msg := bridgeKeyLinkMsg(t, ctx.ChainID(), gonkaPriv, ethPriv, 1)
	_, err := ms.RegisterBridgeKeyLink(ctx, msg)
	require.ErrorIs(t, err, types.ErrPubKeyDoesNotMatchAddress)
}

func TestMsgServer_RegisterBridgeKeyLink_ReplacesLink(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	ethPriv := secp256k1.GenPrivKey()
	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	firstAddr := sdk.AccAddress(first.PubKey().Address())
	secondAddr := sdk.AccAddress(second.PubKey().Address())

	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), firstAddr).Return(authtypes.NewBaseAccount(firstAddr, first.PubKey(), 1, 0)).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), secondAddr).Return(authtypes.NewBaseAccount(secondAddr, second.PubKey(), 1, 0)).AnyTimes()

	_, err := ms.RegisterBridgeKeyLink(ctx, bridgeKeyLinkMsg(t, ctx.ChainID(), first, ethPriv, 1))
	require.NoError(t, err)
	_, err = ms.RegisterBridgeKeyLink(ctx, bridgeKeyLinkMsg(t, ctx.ChainID(), second, ethPriv, 2))
	require.NoError(t, err)

	linked, err := k.ResolveBridgeRecipient(ctx, sdk.AccAddress(ethPriv.PubKey().Address()).String())
	require.NoError(t, err)
	require.Equal(t, secondAddr.String(), linked)
}

func TestHandleNativeTokenRelease_UsesKeyLinkOnce(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)

	ownerPriv := secp256k1.GenPrivKey()
	linkedPriv := secp256k1.GenPrivKey()
	furtherPriv := secp256k1.GenPrivKey()
	owner := sdk.AccAddress(ownerPriv.PubKey().Address())
	linked := sdk.AccAddress(linkedPriv.PubKey().Address())
	further := sdk.AccAddress(furtherPriv.PubKey().Address())
	require.NoError(t, k.BridgeKeyLinks.Set(ctx, owner.String(), types.BridgeKeyLink{OwnerAddress: owner.String(), RecipientAddress: linked.String(), Nonce: 1}))
	require.NoError(t, k.BridgeKeyLinks.Set(ctx, linked.String(), types.BridgeKeyLink{OwnerAddress: linked.String(), RecipientAddress: further.String(), Nonce: 1}))

	recipient, err := k.ResolveBridgeRecipient(ctx, owner.String())
	require.NoError(t, err)
	require.Equal(t, linked.String(), recipient)

	escrow := sdk.AccAddress(bytes.Repeat([]byte{0x11}, 20))
	amount := sdk.NewCoin(types.BaseCoin, math.NewInt(100))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(1000))).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.BridgeEscrowAccName,
		linked,
		sdk.NewCoins(amount),
		"bridge_release",
	).Return(nil).Times(1)

	err = k.HandleNativeTokenRelease(ctx, &types.BridgeTransaction{
		OwnerAddress: owner.String(),
		Amount:       "100",
		ChainId:      "ethereum",
	}, recipient)
	require.NoError(t, err)
}

func TestMsgServer_RegisterBridgeKeyLink_RejectsUppercaseCreator(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	gonkaPriv := secp256k1.GenPrivKey()
	ethPriv := secp256k1.GenPrivKey()
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	msg := bridgeKeyLinkMsg(t, ctx.ChainID(), gonkaPriv, ethPriv, 1)
	msg.Creator = strings.ToUpper(msg.Creator)
	_, err := ms.RegisterBridgeKeyLink(ctx, msg)
	require.ErrorIs(t, err, sdkerrors.ErrInvalidAddress)
}

func TestMsgServer_RegisterBridgeKeyLink_RejectsReplay(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("gonka-test")

	ethPriv := secp256k1.GenPrivKey()
	accountA := secp256k1.GenPrivKey()
	accountB := secp256k1.GenPrivKey()
	addrA := sdk.AccAddress(accountA.PubKey().Address())
	addrB := sdk.AccAddress(accountB.PubKey().Address())
	owner := sdk.AccAddress(ethPriv.PubKey().Address()).String()

	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), addrA).Return(authtypes.NewBaseAccount(addrA, accountA.PubKey(), 1, 0)).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), addrB).Return(authtypes.NewBaseAccount(addrB, accountB.PubKey(), 1, 0)).AnyTimes()

	first := bridgeKeyLinkMsg(t, ctx.ChainID(), accountA, ethPriv, 1)
	_, err := ms.RegisterBridgeKeyLink(ctx, first)
	require.NoError(t, err)
	_, err = ms.RegisterBridgeKeyLink(ctx, bridgeKeyLinkMsg(t, ctx.ChainID(), accountB, ethPriv, 2))
	require.NoError(t, err)

	_, err = ms.RegisterBridgeKeyLink(ctx, first)
	require.ErrorIs(t, err, types.ErrInvalidBridgeKeyLink)

	linked, err := k.ResolveBridgeRecipient(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, addrB.String(), linked)
	stored, err := k.BridgeKeyLinks.Get(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, uint64(2), stored.Nonce)
}

func bridgeKeyLinkMsg(t *testing.T, chainID string, gonkaPriv, ethPriv *secp256k1.PrivKey, nonce uint64) *types.MsgRegisterBridgeKeyLink {
	t.Helper()
	creator := sdk.AccAddress(gonkaPriv.PubKey().Address()).String()
	ethPub := types.CanonicalSecp256k1PubKeyHex(ethPriv.PubKey().Bytes())
	text := types.BridgeKeyLinkSignText(chainID, creator, ethPub, nonce)
	return types.NewMsgRegisterBridgeKeyLink(
		creator,
		ethPub,
		types.CanonicalSecp256k1PubKeyHex(gonkaPriv.PubKey().Bytes()),
		ethereumPersonalSign(t, ethPriv, text),
		nonce,
	)
}

func ethereumPersonalSign(t *testing.T, priv *secp256k1.PrivKey, message string) []byte {
	t.Helper()
	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(message))
	hasher := sha3.NewLegacyKeccak256()
	_, err := hasher.Write(append([]byte(prefix), []byte(message)...))
	require.NoError(t, err)
	sum := hasher.Sum(nil)

	compact := ecdsa.SignCompact(dcrec.PrivKeyFromBytes(priv.Bytes()), sum, false)
	require.Len(t, compact, 65)
	sig := make([]byte, 65)
	copy(sig[:64], compact[1:])
	sig[64] = compact[0]
	return sig
}

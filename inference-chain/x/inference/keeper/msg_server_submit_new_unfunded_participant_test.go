package keeper_test

import (
	"encoding/base64"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMsgServer_SubmitNewUnfundedParticipant_Deprecated(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	encodedPubKey := base64.StdEncoding.EncodeToString(pubKey.Bytes())
	testAddress := sdk.AccAddress(pubKey.Address()).String()

	// Neither account nor participant state may be written now that MsgSubmitNewUnfundedParticipant is suspended.
	mocks.AccountKeeper.EXPECT().NewAccountWithAddress(gomock.Any(), gomock.Any()).Times(0)
	mocks.AccountKeeper.EXPECT().SetAccount(gomock.Any(), gomock.Any()).Times(0)

	_, err := ms.SubmitNewUnfundedParticipant(ctx, &types.MsgSubmitNewUnfundedParticipant{
		Creator: testutil.Creator,
		Address: testAddress,
		PubKey:  encodedPubKey,
		Url:     "inference-url",
	})
	require.ErrorIs(t, err, types.ErrDeprecated)

	_, found := k.GetParticipant(ctx, testAddress)
	require.False(t, found, "participant must not be created when MsgSubmitNewUnfundedParticipant is deprecated")
}

func TestMsgServer_SubmitNewUnfundedParticipant_RejectsOffCurvePubKeyWithoutStateWrites(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Report vector: 02 || 31*00 || 05 (33 bytes, 0x02 prefix, off-curve)
	offCurveBytes := make([]byte, 33)
	offCurveBytes[0] = 0x02
	offCurveBytes[32] = 0x05
	offCurvePubKey := secp256k1.PubKey{Key: offCurveBytes}
	derivedAddress := sdk.AccAddress(offCurvePubKey.Address()).String()
	encodedOffCurve := base64.StdEncoding.EncodeToString(offCurveBytes)

	// Neither NewAccountWithAddress nor SetAccount should be called.
	mocks.AccountKeeper.EXPECT().NewAccountWithAddress(gomock.Any(), gomock.Any()).Times(0)
	mocks.AccountKeeper.EXPECT().SetAccount(gomock.Any(), gomock.Any()).Times(0)

	msg := &types.MsgSubmitNewUnfundedParticipant{
		Creator: testutil.Creator,
		Address: derivedAddress,
		PubKey:  encodedOffCurve,
		Url:     "https://inference.example.com",
	}
	require.ErrorContains(t, msg.ValidateBasic(), "not a valid curve point")

	_, err := ms.SubmitNewUnfundedParticipant(ctx, msg)
	require.ErrorIs(t, err, types.ErrDeprecated)

	_, found := k.GetParticipant(ctx, derivedAddress)
	require.False(t, found, "participant must not be created for an off-curve public key")
}



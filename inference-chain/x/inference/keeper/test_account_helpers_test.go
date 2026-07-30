package keeper_test

import (
	"context"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const MODEL_ID = "Qwen/QwQ-32B"

type MockAccount struct {
	*authtypes.BaseAccount
	key     *secp256k1.PrivKey
	address string
}

var _ sdk.AccountI = (*MockAccount)(nil)

func NewMockAccount(address string) *MockAccount {
	addr := sdk.MustAccAddressFromBech32(address)
	key := secp256k1.GenPrivKey()
	baseAccount := authtypes.NewBaseAccountWithAddress(addr)
	_ = baseAccount.SetPubKey(key.PubKey())
	return &MockAccount{
		BaseAccount: baseAccount,
		key:         key,
		address:     address,
	}
}

func (m MockAccount) GetAddress() sdk.AccAddress {
	return sdk.MustAccAddressFromBech32(m.address)
}

func (m MockAccount) SetAddress(sdk.AccAddress) error {
	return nil
}

func (m MockAccount) GetPubKey() cryptotypes.PubKey {
	return m.key.PubKey()
}

func (m MockAccount) SetPubKey(cryptotypes.PubKey) error {
	return nil
}

func (m MockAccount) GetAccountNumber() uint64 {
	return 0
}

func (m MockAccount) SetAccountNumber(uint64) error {
	return nil
}

func (m MockAccount) GetSequence() uint64 {
	return 0
}

func (m MockAccount) SetSequence(uint64) error {
	return nil
}

func (m MockAccount) String() string {
	return m.address
}

func (m MockAccount) GetBechAddress() sdk.AccAddress {
	return m.GetAddress()
}

func MustAddParticipant(t require.TestingT, ms types.MsgServer, ctx context.Context, mockAccount MockAccount) {
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      mockAccount.address,
		Url:          "url",
		ValidatorKey: mockAccount.GetPubKey().String(),
	})
	require.NoError(t, err)
}

func AddParticipantToActive(ctx sdk.Context, k *keeper.Keeper, address string, epochID int64) {
	participant := types.Participant{
		Index:   address,
		Address: address,
		Status:  types.ParticipantStatus_ACTIVE,
	}
	_ = k.SetParticipant(ctx, participant)
	_ = k.SetActiveParticipants(ctx, ParticipantsToActive(epochID, participant))
}

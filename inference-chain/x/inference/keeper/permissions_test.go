package keeper_test

import (
	"reflect"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// test message types used for targeted permission checks
type testMsgSingleSigner struct{ signer string }

func (m *testMsgSingleSigner) GetSignersStrings() []string { return []string{m.signer} }

func (m *testMsgSingleSigner) ValidateBasic() error { return nil }

// Utility to get msgServer and context for tests that need to call CheckPermission directly.
func setupPermissionsHarness(t *testing.T) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	// bech32 config for tests
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	ms := keeper.NewMsgServerImpl(k)
	return k, ms, ctx, &mocks
}

func TestPermission_Governance(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	// happy path: signer equals authority
	gov := k.GetAuthority()
	msg := &types.MsgUpdateParams{Authority: gov, Params: types.DefaultParams()}
	err := keeper.CheckPermission(ms, ctx, msg, keeper.GovernancePermission)
	require.NoError(t, err)

	// negative: signer not authority
	notGov := testutil.Bech32Addr(100)
	msg2 := &types.MsgUpdateParams{Authority: notGov, Params: types.DefaultParams()}
	err = keeper.CheckPermission(ms, ctx, msg2, keeper.GovernancePermission)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrInvalidSigner)
}

func TestPermission_Account(t *testing.T) {
	_, ms, ctx, mocks := setupPermissionsHarness(t)

	// message requiring AccountPermission
	msg := types.NewMsgBridgeExchange(testutil.Validator, "eth", "0x1", "0x2", "pk", "1", "2", "3", "4")

	// success: account exists
	accAddr := sdk.MustAccAddressFromBech32(testutil.Validator)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), accAddr).Return(true).Times(1)
	err := keeper.CheckPermission(ms, ctx, msg, keeper.AccountPermission)
	require.NoError(t, err)

	// failure: account not found
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), accAddr).Return(false).Times(1)
	err = keeper.CheckPermission(ms, ctx, msg, keeper.AccountPermission)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrAccountNotFound)
}

func TestPermission_Participant(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	signer := testutil.Executor
	msg := &types.MsgSubmitSeed{Creator: signer, EpochIndex: 1, Signature: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	// failure: participant not in store
	err := keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.Error(t, err)

	// success: add participant and recheck
	p := types.Participant{Index: signer, Address: signer}
	require.NoError(t, k.Participants.Set(ctx, sdk.MustAccAddressFromBech32(signer), p))
	err = keeper.CheckPermission(ms, ctx, msg, keeper.ParticipantPermission)
	require.NoError(t, err)
}

func TestPermission_ActiveParticipant_CurrentAndPrevious(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	signer := testutil.Validator
	type testActiveMsg struct{ testMsgSingleSigner }
	keeper.MessagePermissions[reflect.TypeOf((*testActiveMsg)(nil))] = []keeper.Permission{keeper.ActiveParticipantPermission}
	msgActive := &testActiveMsg{testMsgSingleSigner{signer: signer}}

	// set current epoch
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 10))

	// failure: no active set for current epoch
	err := keeper.CheckPermission(ms, ctx, msgActive, keeper.ActiveParticipantPermission)
	require.Error(t, err)

	// success: add active participants for current epoch
	ap := types.ActiveParticipants{EpochId: 10, Participants: []*types.ActiveParticipant{{Index: signer}}}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))
	err = keeper.CheckPermission(ms, ctx, msgActive, keeper.ActiveParticipantPermission)
	require.NoError(t, err)

	// previous active permission: map uses MsgValidation with OR [Active, PreviousActive]
	msgVal := &types.MsgValidation{Creator: signer}
	// still OK because active in current epoch
	err = keeper.CheckPermission(ms, ctx, msgVal, keeper.ActiveParticipantPermission, keeper.PreviousActiveParticipantPermission)
	require.NoError(t, err)

	// move active set to previous epoch only (epoch 9 contains signer; epoch 10 does not)
	apPrev := types.ActiveParticipants{EpochId: 9, Participants: []*types.ActiveParticipant{{Index: signer}}}
	require.NoError(t, k.SetActiveParticipants(ctx, apPrev))
	// ensure current epoch is 10
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 10))
	// We also need to clear current active participants cache for epoch 10 if it was set before
	require.NoError(t, k.ActiveParticipantsSet.Remove(ctx, collections.Join(uint64(10), sdk.MustAccAddressFromBech32(signer))))

	// check OR: should pass because PreviousActive holds even if Active doesn't
	err = keeper.CheckPermission(ms, ctx, msgVal, keeper.ActiveParticipantPermission, keeper.PreviousActiveParticipantPermission)
	require.NoError(t, err)

	// negative: neither current nor previous contains signer
	emptyCurrent := types.ActiveParticipants{EpochId: 10, Participants: []*types.ActiveParticipant{}}
	require.NoError(t, k.SetActiveParticipants(ctx, emptyCurrent))
	// Clear the cache manually because SetActiveParticipants with empty list doesn't clear it
	require.NoError(t, k.ActiveParticipantsSet.Remove(ctx, collections.Join(uint64(10), sdk.MustAccAddressFromBech32(signer))))
	emptyPrevious := types.ActiveParticipants{EpochId: 9, Participants: []*types.ActiveParticipant{}}
	require.NoError(t, k.SetActiveParticipants(ctx, emptyPrevious))
	// Clear the cache manually
	require.NoError(t, k.ActiveParticipantsSet.Remove(ctx, collections.Join(uint64(9), sdk.MustAccAddressFromBech32(signer))))
	err = keeper.CheckPermission(ms, ctx, msgVal, keeper.ActiveParticipantPermission, keeper.PreviousActiveParticipantPermission)
	require.Error(t, err)
}

func TestPermission_CurrentActiveParticipant(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	// Create a test-only message mapped to CurrentActiveParticipantPermission
	type testCurrentActiveMsg struct{ testMsgSingleSigner }
	// register mapping for this test type
	keeper.MessagePermissions[reflect.TypeOf((*testCurrentActiveMsg)(nil))] = []keeper.Permission{keeper.CurrentActiveParticipantPermission}

	signer := testutil.Validator
	signerAddr := sdk.MustAccAddressFromBech32(signer)
	// set epoch and active set with signer
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 7))
	ap := types.ActiveParticipants{EpochId: 7, Participants: []*types.ActiveParticipant{{Index: signer}}}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))

	// success: not excluded
	msg := &testCurrentActiveMsg{testMsgSingleSigner{signer: signer}}
	err := keeper.CheckPermission(ms, ctx, msg, keeper.CurrentActiveParticipantPermission)
	require.NoError(t, err)

	// failure: excluded in current epoch
	require.NoError(t, k.ExcludedParticipantsMap.Set(ctx, collections.Join(uint64(7), signerAddr), types.ExcludedParticipant{EpochIndex: 7, Address: signer}))
	err = keeper.CheckPermission(ms, ctx, msg, keeper.CurrentActiveParticipantPermission)
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrParticipantNotFound)
}

func TestPermission_TrainingAllowLists(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	signer := testutil.Executor
	signerAddr := sdk.MustAccAddressFromBech32(signer)

	// Exec permission
	execMsg := &types.MsgTrainingHeartbeat{Creator: signer}
	// failure when not allowed
	err := keeper.CheckPermission(ms, ctx, execMsg, keeper.TrainingExecPermission)
	require.Error(t, err)
	// allow and succeed
	require.NoError(t, k.TrainingExecAllowListSet.Set(ctx, signerAddr))
	err = keeper.CheckPermission(ms, ctx, execMsg, keeper.TrainingExecPermission)
	require.NoError(t, err)

	// Start permission
	startMsg := &types.MsgCreateDummyTrainingTask{Creator: signer}
	// failure when not allowed
	err = keeper.CheckPermission(ms, ctx, startMsg, keeper.TrainingStartPermission)
	require.Error(t, err)
	// allow and succeed
	require.NoError(t, k.TrainingStartAllowListSet.Set(ctx, signerAddr))
	err = keeper.CheckPermission(ms, ctx, startMsg, keeper.TrainingStartPermission)
	require.NoError(t, err)
}

func TestPermission_NoPermissionAlwaysPasses(t *testing.T) {
	_, ms, ctx, _ := setupPermissionsHarness(t)
	msg := &types.MsgInvalidateInference{Creator: testutil.Requester, InferenceId: "id"}
	err := keeper.CheckPermission(ms, ctx, msg, keeper.NoPermission)
	require.NoError(t, err)
}

func TestPermission_OR_Semantics(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)

	signer := testutil.Validator
	// prepare epochs
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 100))

	// Only previous active contains signer
	prev := types.ActiveParticipants{EpochId: 99, Participants: []*types.ActiveParticipant{{Index: signer}}}
	require.NoError(t, k.SetActiveParticipants(ctx, prev))

	msg := &types.MsgValidation{Creator: signer}
	// Should pass because PreviousActive satisfies OR
	err := keeper.CheckPermission(ms, ctx, msg, keeper.ActiveParticipantPermission, keeper.PreviousActiveParticipantPermission)
	require.NoError(t, err)

	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 105))
	err = keeper.CheckPermission(ms, ctx, msg, keeper.ActiveParticipantPermission, keeper.PreviousActiveParticipantPermission)
	require.Error(t, err)
}

func TestPermission_InvalidSignerAddress(t *testing.T) {
	_, ms, ctx, _ := setupPermissionsHarness(t)

	// Use a governance-mapped msg but put invalid bech32 signer
	msg := &types.MsgUpdateParams{Authority: "not_bech32", Params: types.DefaultParams()}
	err := keeper.CheckPermission(ms, ctx, msg, keeper.GovernancePermission)
	require.Error(t, err)
}

func TestPermission_Contract_Placeholder(t *testing.T) {
	t.Skip("ContractPermission requires a Wasm keeper; integrate wasm test keeper if/when available")
}

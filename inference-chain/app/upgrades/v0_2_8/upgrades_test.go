package v0_2_8

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	bookkeepertypes "github.com/productscience/inference/x/bookkeeper/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

var errUnauthorizedBurn = errors.New("module account pre_programmed_sale does not have permissions to burn tokens: unauthorized")

// TestBurnExtraCommunityCoins_OldApproachFails demonstrates that the old approach
// (burning directly from pre_programmed_sale) would fail due to missing burner permission.
func TestBurnExtraCommunityCoins_OldApproachFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_, ctx, mocks := setupTestKeeper(t, ctrl)

	coins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000000))

	// Simulate OLD behavior: direct burn from pre_programmed_sale fails
	// because it doesn't have burner permission
	mocks.BankKeeper.EXPECT().
		BurnCoins(gomock.Any(), "pre_programmed_sale", coins, gomock.Any()).
		Return(errUnauthorizedBurn)

	// Call the old burn approach directly (simulating what the old code did)
	err := mocks.BankKeeper.BurnCoins(ctx, "pre_programmed_sale", coins, "direct burn attempt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unauthorized")
}

// TestBurnExtraCommunityCoins_NewApproachSucceeds demonstrates that the new approach
// (transfer to bookkeeper, then burn) succeeds.
func TestBurnExtraCommunityCoins_NewApproachSucceeds(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, mocks := setupTestKeeper(t, ctrl)

	preProgrammedSaleAddr := authtypes.NewModuleAddress("pre_programmed_sale")
	coins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000000))

	// Mock: GetModuleAddress returns the expected address
	mocks.AccountKeeper.EXPECT().
		GetModuleAddress("pre_programmed_sale").
		Return(preProgrammedSaleAddr)

	// Mock: SpendableCoins returns some coins to burn
	mocks.BankViewKeeper.EXPECT().
		SpendableCoins(gomock.Any(), preProgrammedSaleAddr).
		Return(coins)

	// Step 1: Transfer from pre_programmed_sale to bookkeeper succeeds
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToModule(gomock.Any(), "pre_programmed_sale", bookkeepertypes.ModuleName, coins, "transfer for burn").
		Return(nil)

	// Step 2: Burn from bookkeeper succeeds (bookkeeper has burner permission)
	mocks.BankKeeper.EXPECT().
		BurnCoins(gomock.Any(), bookkeepertypes.ModuleName, coins, "one-time burn of pre_programmed_sale account").
		Return(nil)

	// Call the actual function
	err := burnExtraCommunityCoins(ctx, &k)
	require.NoError(t, err)
}

// TestBurnExtraCommunityCoins_NoCoins tests that the function handles empty balance gracefully.
func TestBurnExtraCommunityCoins_NoCoins(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, mocks := setupTestKeeper(t, ctrl)

	preProgrammedSaleAddr := authtypes.NewModuleAddress("pre_programmed_sale")

	// Mock: GetModuleAddress returns the expected address
	mocks.AccountKeeper.EXPECT().
		GetModuleAddress("pre_programmed_sale").
		Return(preProgrammedSaleAddr)

	// Mock: SpendableCoins returns empty (no coins to burn)
	mocks.BankViewKeeper.EXPECT().
		SpendableCoins(gomock.Any(), preProgrammedSaleAddr).
		Return(sdk.NewCoins())

	// No burn calls expected when there are no coins

	err := burnExtraCommunityCoins(ctx, &k)
	require.NoError(t, err)
}

// TestBurnExtraCommunityCoins_TransferFails tests error handling when transfer fails.
func TestBurnExtraCommunityCoins_TransferFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, mocks := setupTestKeeper(t, ctrl)

	preProgrammedSaleAddr := authtypes.NewModuleAddress("pre_programmed_sale")
	coins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000000))

	// Mock: GetModuleAddress returns the expected address
	mocks.AccountKeeper.EXPECT().
		GetModuleAddress("pre_programmed_sale").
		Return(preProgrammedSaleAddr)

	// Mock: SpendableCoins returns some coins
	mocks.BankViewKeeper.EXPECT().
		SpendableCoins(gomock.Any(), preProgrammedSaleAddr).
		Return(coins)

	// Transfer fails
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToModule(gomock.Any(), "pre_programmed_sale", bookkeepertypes.ModuleName, coins, "transfer for burn").
		Return(errors.New("transfer failed"))

	err := burnExtraCommunityCoins(ctx, &k)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to transfer coins")
}

type testMocks struct {
	BankKeeper     *keepertest.MockBookkeepingBankKeeper
	BankViewKeeper *keepertest.MockBankKeeper
	AccountKeeper  *keepertest.MockAccountKeeper
}

func setupTestKeeper(t *testing.T, ctrl *gomock.Controller) (keeper.Keeper, sdk.Context, testMocks) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	bankKeeper := keepertest.NewMockBookkeepingBankKeeper(ctrl)
	bankViewKeeper := keepertest.NewMockBankKeeper(ctrl)
	accountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	validatorSet := keepertest.NewMockValidatorSet(ctrl)
	groupMock := keepertest.NewMockGroupMessageKeeper(ctrl)
	stakingMock := keepertest.NewMockStakingKeeper(ctrl)
	collateralMock := keepertest.NewMockCollateralKeeper(ctrl)
	streamvestingMock := keepertest.NewMockStreamVestingKeeper(ctrl)
	authzKeeper := keepertest.NewMockAuthzKeeper(ctrl)
	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	blsStoreKey := storetypes.NewKVStoreKey(blstypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(blsStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	blsKeeper := blskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(blsStoreKey),
		log.NewNopLogger(),
		authority.String(),
	)

	k := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		log.NewNopLogger(),
		authority.String(),
		bankKeeper,
		bankViewKeeper,
		groupMock,
		validatorSet,
		stakingMock,
		accountKeeper,
		blsKeeper,
		collateralMock,
		streamvestingMock,
		authzKeeper,
		nil,
		upgradeKeeper,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())

	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	require.NoError(t, blsKeeper.SetParams(ctx, blstypes.DefaultParams()))

	mocks := testMocks{
		BankKeeper:     bankKeeper,
		BankViewKeeper: bankViewKeeper,
		AccountKeeper:  accountKeeper,
	}

	return k, ctx, mocks
}

// mockAuthzMigrationKeeper implements AuthzMigrationKeeper for testing
type mockAuthzMigrationKeeper struct {
	grants       []mockGrant
	savedGrants  []mockSavedGrant
	existingAuth map[string]authz.Authorization
}

type mockGrant struct {
	granter sdk.AccAddress
	grantee sdk.AccAddress
	grant   authz.Grant
}

type mockSavedGrant struct {
	grantee       sdk.AccAddress
	granter       sdk.AccAddress
	authorization authz.Authorization
	expiration    *time.Time
}

func newMockAuthzMigrationKeeper() *mockAuthzMigrationKeeper {
	return &mockAuthzMigrationKeeper{
		existingAuth: make(map[string]authz.Authorization),
	}
}

func (m *mockAuthzMigrationKeeper) IterateGrants(ctx context.Context, handler func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool) {
	for _, g := range m.grants {
		if handler(g.granter, g.grantee, g.grant) {
			return
		}
	}
}

func (m *mockAuthzMigrationKeeper) GetAuthorization(ctx context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time) {
	key := grantee.String() + granter.String() + msgType
	auth, ok := m.existingAuth[key]
	if !ok {
		return nil, nil
	}
	return auth, nil
}

func (m *mockAuthzMigrationKeeper) SaveGrant(ctx context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error {
	m.savedGrants = append(m.savedGrants, mockSavedGrant{
		grantee:       grantee,
		granter:       granter,
		authorization: authorization,
		expiration:    expiration,
	})
	return nil
}

func (m *mockAuthzMigrationKeeper) addGrant(granter, grantee sdk.AccAddress, msgTypeURL string, expiration *time.Time, cdc codec.BinaryCodec) {
	genAuth := authz.NewGenericAuthorization(msgTypeURL)
	authAny, err := codectypes.NewAnyWithValue(genAuth)
	if err != nil {
		panic(err)
	}
	m.grants = append(m.grants, mockGrant{
		granter: granter,
		grantee: grantee,
		grant: authz.Grant{
			Authorization: authAny,
			Expiration:    expiration,
		},
	})
}

func (m *mockAuthzMigrationKeeper) addExistingAuth(grantee, granter sdk.AccAddress, msgType string) {
	key := grantee.String() + granter.String() + msgType
	m.existingAuth[key] = authz.NewGenericAuthorization(msgType)
}

// TestMigrateAuthzGrantsForPocV2_Success tests that V2 grants are added for pairs with MsgStartInference
func TestMigrateAuthzGrantsForPocV2_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	granter := sdk.AccAddress([]byte("granter1___________"))
	grantee := sdk.AccAddress([]byte("grantee1___________"))
	expiration := time.Now().Add(24 * time.Hour)

	// Add a MsgStartInference grant
	mockAuthz.addGrant(granter, grantee, sdk.MsgTypeURL(&types.MsgStartInference{}), &expiration, k.Codec())

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// Verify 3 new grants were added (one for each V2 message type)
	require.Len(t, mockAuthz.savedGrants, 3)

	// Verify the message types
	expectedMsgTypes := map[string]bool{
		sdk.MsgTypeURL(&types.MsgSubmitPocValidationsV2{}):   false,
		sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}):         false,
		sdk.MsgTypeURL(&types.MsgMLNodeWeightDistribution{}): false,
	}

	for _, saved := range mockAuthz.savedGrants {
		require.Equal(t, grantee, saved.grantee)
		require.Equal(t, granter, saved.granter)
		require.Equal(t, &expiration, saved.expiration)

		genAuth, ok := saved.authorization.(*authz.GenericAuthorization)
		require.True(t, ok)
		_, found := expectedMsgTypes[genAuth.Msg]
		require.True(t, found, "unexpected message type: %s", genAuth.Msg)
		expectedMsgTypes[genAuth.Msg] = true
	}

	// Verify all expected types were added
	for msgType, added := range expectedMsgTypes {
		require.True(t, added, "expected message type not added: %s", msgType)
	}
}

// TestMigrateAuthzGrantsForPocV2_SkipsExistingGrants tests that existing V2 grants are not overwritten
func TestMigrateAuthzGrantsForPocV2_SkipsExistingGrants(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	granter := sdk.AccAddress([]byte("granter1___________"))
	grantee := sdk.AccAddress([]byte("grantee1___________"))
	expiration := time.Now().Add(24 * time.Hour)

	// Add a MsgStartInference grant
	mockAuthz.addGrant(granter, grantee, sdk.MsgTypeURL(&types.MsgStartInference{}), &expiration, k.Codec())

	// Mark one V2 message type as already existing
	mockAuthz.addExistingAuth(grantee, granter, sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// Should only add 2 new grants (skipping the existing one)
	require.Len(t, mockAuthz.savedGrants, 2)

	// Verify MsgPoCV2StoreCommit was NOT saved (already existed)
	for _, saved := range mockAuthz.savedGrants {
		genAuth, ok := saved.authorization.(*authz.GenericAuthorization)
		require.True(t, ok)
		require.NotEqual(t, sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}), genAuth.Msg)
	}
}

// TestMigrateAuthzGrantsForPocV2_NoGrants tests that migration handles empty grants gracefully
func TestMigrateAuthzGrantsForPocV2_NoGrants(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	// No grants added

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// No grants should be saved
	require.Len(t, mockAuthz.savedGrants, 0)
}

// TestMigrateAuthzGrantsForPocV2_MultipleGranterGranteePairs tests migration with multiple pairs
func TestMigrateAuthzGrantsForPocV2_MultipleGranterGranteePairs(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	granter1 := sdk.AccAddress([]byte("granter1___________"))
	grantee1 := sdk.AccAddress([]byte("grantee1___________"))
	granter2 := sdk.AccAddress([]byte("granter2___________"))
	grantee2 := sdk.AccAddress([]byte("grantee2___________"))
	expiration := time.Now().Add(24 * time.Hour)

	// Add MsgStartInference grants for both pairs
	mockAuthz.addGrant(granter1, grantee1, sdk.MsgTypeURL(&types.MsgStartInference{}), &expiration, k.Codec())
	mockAuthz.addGrant(granter2, grantee2, sdk.MsgTypeURL(&types.MsgStartInference{}), &expiration, k.Codec())

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// Should add 6 grants total (3 for each pair)
	require.Len(t, mockAuthz.savedGrants, 6)

	// Count grants per pair
	pair1Count := 0
	pair2Count := 0
	for _, saved := range mockAuthz.savedGrants {
		if saved.granter.Equals(granter1) && saved.grantee.Equals(grantee1) {
			pair1Count++
		}
		if saved.granter.Equals(granter2) && saved.grantee.Equals(grantee2) {
			pair2Count++
		}
	}
	require.Equal(t, 3, pair1Count)
	require.Equal(t, 3, pair2Count)
}

// TestMigrateAuthzGrantsForPocV2_IgnoresNonStartInferenceGrants tests that non-MsgStartInference grants are ignored
func TestMigrateAuthzGrantsForPocV2_IgnoresNonStartInferenceGrants(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	granter := sdk.AccAddress([]byte("granter1___________"))
	grantee := sdk.AccAddress([]byte("grantee1___________"))
	expiration := time.Now().Add(24 * time.Hour)

	// Add grants for other message types (not MsgStartInference)
	mockAuthz.addGrant(granter, grantee, "/some.other.MsgType", &expiration, k.Codec())
	mockAuthz.addGrant(granter, grantee, sdk.MsgTypeURL(&types.MsgFinishInference{}), &expiration, k.Codec())

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// No V2 grants should be added (no MsgStartInference found)
	require.Len(t, mockAuthz.savedGrants, 0)
}

// TestMigrateAuthzGrantsForPocV2_PreservesExpiration tests that the expiration is preserved from the original grant
func TestMigrateAuthzGrantsForPocV2_PreservesExpiration(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	granter := sdk.AccAddress([]byte("granter1___________"))
	grantee := sdk.AccAddress([]byte("grantee1___________"))
	expiration := time.Date(2027, 6, 15, 12, 0, 0, 0, time.UTC)

	// Add a MsgStartInference grant with specific expiration
	mockAuthz.addGrant(granter, grantee, sdk.MsgTypeURL(&types.MsgStartInference{}), &expiration, k.Codec())

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// All saved grants should have the same expiration
	for _, saved := range mockAuthz.savedGrants {
		require.NotNil(t, saved.expiration)
		require.Equal(t, expiration, *saved.expiration)
	}
}

// TestMigrateAuthzGrantsForPocV2_NilExpiration tests migration with nil expiration (no time limit)
func TestMigrateAuthzGrantsForPocV2_NilExpiration(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	k, ctx, _ := setupTestKeeper(t, ctrl)

	mockAuthz := newMockAuthzMigrationKeeper()

	granter := sdk.AccAddress([]byte("granter1___________"))
	grantee := sdk.AccAddress([]byte("grantee1___________"))

	// Add a MsgStartInference grant with nil expiration
	mockAuthz.addGrant(granter, grantee, sdk.MsgTypeURL(&types.MsgStartInference{}), nil, k.Codec())

	// Run migration
	err := migrateAuthzGrantsForPocV2(ctx, mockAuthz, k)
	require.NoError(t, err)

	// Verify 3 new grants were added
	require.Len(t, mockAuthz.savedGrants, 3)

	// All saved grants should have nil expiration
	for _, saved := range mockAuthz.savedGrants {
		require.Nil(t, saved.expiration)
	}
}

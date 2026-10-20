package inference_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"cosmossdk.io/math"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	hashToCurve "github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/sha3"

	keepertest "github.com/productscience/inference/testutil/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

// setupTestKeeperWithBLS creates a test keeper with BLS integration
func setupTestKeeperWithBLS(t testing.TB) (keeper.Keeper, sdk.Context) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)
	return k, ctx
}

func allowEmptyBLSGrantQueries(mocks keepertest.InferenceMocks) {
	mocks.AuthzKeeper.EXPECT().
		GranterGrants(gomock.Any(), gomock.Any()).
		Return(&authztypes.QueryGranterGrantsResponse{}, nil).
		AnyTimes()
}

// newSecp256k1PubKeyFromHexStr creates a secp256k1.PubKey from a hex string (compressed, 33 bytes).
func newSecp256k1PubKeyFromHexStr(t *testing.T, hexStr string) cryptotypes.PubKey {
	bz, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	pubKey := &secp256k1.PubKey{Key: bz}
	// Basic validation: ensure the key is not nil and bytes are not empty.
	// More specific secp256k1 validation (like length) can be added if necessary,
	// but for mock setup, this is often sufficient.
	require.NotNil(t, pubKey, "Public key should not be nil after creation from hex")
	require.NotEmpty(t, pubKey.Bytes(), "Public key bytes should not be empty")
	return pubKey
}

// generateValidBech32Address generates a valid bech32 address from a public key hex string
func generateValidBech32Address(t *testing.T, pubKeyHex string) string {
	pubKey := newSecp256k1PubKeyFromHexStr(t, pubKeyHex)
	addr := sdk.AccAddress(pubKey.Address())
	return addr.String()
}

var (
	// Valid compressed secp256k1 public keys (33 bytes each)
	aliceSecp256k1PubHex      = "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	bobSecp256k1PubHex        = "02c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	charlieSecp256k1PubHex    = "031884e5018572688f308999f53092837489aeac31afe1389809281562794c171b"
	aliceOtherSecp256k1PubHex = "020f6fcfcbd42b6b7ad4c5e5df6c0e57b82e1c7b2b6f4c45f0b7a8b5c2d1e0f3"

	// Generate valid bech32 addresses from the public keys
	aliceAccAddrStr   string
	bobAccAddrStr     string
	charlieAccAddrStr string
)

// setupSDKConfig configures the SDK for testing if not already configured
func setupSDKConfig() {
	config := sdk.GetConfig()
	// Only configure if not already configured with gonka prefix
	if config.GetBech32AccountAddrPrefix() != "gonka" {
		config.SetBech32PrefixForAccount("gonka", "gonkapub")
		config.SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")
	}
}

// setupTestAddresses generates valid bech32 addresses for testing
func setupTestAddresses(t *testing.T) {
	setupSDKConfig() // Ensure SDK is configured before generating addresses
	if aliceAccAddrStr == "" {
		aliceAccAddrStr = generateValidBech32Address(t, aliceSecp256k1PubHex)
		bobAccAddrStr = generateValidBech32Address(t, bobSecp256k1PubHex)
		charlieAccAddrStr = generateValidBech32Address(t, charlieSecp256k1PubHex)
	}
}

// setupMockAccountExpectations configures the MockAccountKeeper with expected accounts and their public keys.
// It returns a map of address strings to their expected public key bytes for easy verification.
func setupMockAccountExpectations(t *testing.T, mockAK *keepertest.MockAccountKeeper, participantsDetails map[string]string) map[string][]byte {
	expectedPubKeysBytes := make(map[string][]byte)

	for addrStr, pubKeyHex := range participantsDetails {
		addr, err := sdk.AccAddressFromBech32(addrStr)
		require.NoError(t, err)

		if pubKeyHex == "" { // Simulate account with no public key
			baseAcc := authtypes.NewBaseAccountWithAddress(addr)
			baseAcc.SetAccountNumber(1) // Required for some operations, not strictly for GetPubKey
			mockAK.EXPECT().GetAccount(gomock.Any(), addr).Return(baseAcc).AnyTimes()
			expectedPubKeysBytes[addrStr] = nil // Explicitly nil for accounts with no pubkey
		} else if pubKeyHex == "nil" { // Simulate account not found
			mockAK.EXPECT().GetAccount(gomock.Any(), addr).Return(nil).AnyTimes()
			expectedPubKeysBytes[addrStr] = nil // Explicitly nil for not found accounts
		} else {
			pubKey := newSecp256k1PubKeyFromHexStr(t, pubKeyHex)
			baseAcc := authtypes.NewBaseAccount(addr, pubKey, 0, 0)
			mockAK.EXPECT().GetAccount(gomock.Any(), addr).Return(baseAcc).AnyTimes()
			expectedPubKeysBytes[addrStr] = pubKey.Bytes()
		}
	}
	return expectedPubKeysBytes
}

func TestBLSKeyGenerationIntegration(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	participantDetails := map[string]string{
		aliceAccAddrStr:   aliceSecp256k1PubHex,
		bobAccAddrStr:     bobSecp256k1PubHex,
		charlieAccAddrStr: charlieSecp256k1PubHex,
	}
	expectedPubKeysMap := setupMockAccountExpectations(t, mockAccountKeeper, participantDetails)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	participants := []*types.Participant{
		{Index: aliceAccAddrStr, Address: aliceAccAddrStr, ValidatorKey: "valKeyAlice", WorkerPublicKey: "ignoredWKeyAlice", Weight: 50, Status: types.ParticipantStatus_ACTIVE},
		{Index: bobAccAddrStr, Address: bobAccAddrStr, ValidatorKey: "valKeyBob", WorkerPublicKey: "ignoredWKeyBob", Weight: 30, Status: types.ParticipantStatus_ACTIVE},
		{Index: charlieAccAddrStr, Address: charlieAccAddrStr, ValidatorKey: "valKeyCharlie", WorkerPublicKey: "ignoredWKeyCharlie", Weight: 20, Status: types.ParticipantStatus_ACTIVE},
	}
	for _, p := range participants {
		k.SetParticipant(ctx, *p)
	}

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 50, CapWeight: 50},
		{Index: bobAccAddrStr, Weight: 30, CapWeight: 30},
		{Index: charlieAccAddrStr, Weight: 20, CapWeight: 20},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(1)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	for _, p := range epochBLSData.Participants {
		expectedBytes, ok := expectedPubKeysMap[p.Address]
		require.True(t, ok)
		require.Equal(t, expectedBytes, p.Secp256K1PublicKey)
	}
}

func TestBLSKeyGenerationUsesCapWeightForSlots(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	participantDetails := map[string]string{
		aliceAccAddrStr:   aliceSecp256k1PubHex,
		bobAccAddrStr:     bobSecp256k1PubHex,
		charlieAccAddrStr: charlieSecp256k1PubHex,
	}
	setupMockAccountExpectations(t, mockAccountKeeper, participantDetails)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	for _, p := range []*types.Participant{
		{Index: aliceAccAddrStr, Address: aliceAccAddrStr, Weight: 50, Status: types.ParticipantStatus_ACTIVE},
		{Index: bobAccAddrStr, Address: bobAccAddrStr, Weight: 30, Status: types.ParticipantStatus_ACTIVE},
		{Index: charlieAccAddrStr, Address: charlieAccAddrStr, Weight: 100, Status: types.ParticipantStatus_ACTIVE},
	} {
		k.SetParticipant(ctx, *p)
	}

	// Alice/Bob have proven trust weight; Charlie is new (CapWeight 0) despite
	// a large real Weight. Slot assignment must follow CapWeight, not Weight.
	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 50, CapWeight: 40},
		{Index: bobAccAddrStr, Weight: 30, CapWeight: 30},
		{Index: charlieAccAddrStr, Weight: 100, CapWeight: 0},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(21)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)

	byAddr := map[string]blstypes.BLSParticipantInfo{}
	for _, p := range epochBLSData.Participants {
		byAddr[p.Address] = p
	}
	_, charliePresent := byAddr[charlieAccAddrStr]
	require.False(t, charliePresent, "zero-cap participant must receive no BLS slots")

	alice, ok := byAddr[aliceAccAddrStr]
	require.True(t, ok)
	bob, ok := byAddr[bobAccAddrStr]
	require.True(t, ok)

	totalCap := math.LegacyNewDec(70)
	require.True(t, alice.PercentageWeight.Equal(math.LegacyNewDec(40).Quo(totalCap).Mul(math.LegacyNewDec(100))))
	require.True(t, bob.PercentageWeight.Equal(math.LegacyNewDec(30).Quo(totalCap).Mul(math.LegacyNewDec(100))))

	aliceSlots := alice.SlotEndIndex - alice.SlotStartIndex + 1
	bobSlots := bob.SlotEndIndex - bob.SlotStartIndex + 1
	require.Equal(t, epochBLSData.ITotalSlots, aliceSlots+bobSlots)
	require.Greater(t, aliceSlots, bobSlots)
}

func TestBLSKeyGenerationWithEmptyParticipants(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)

	epochID := uint64(2)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, []*types.ActiveParticipant{})

	_, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.Error(t, err)
}

func TestBLSKeyGenerationWithAccountKeyIssues(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	// Alice: No account found (GetAccount returns nil)
	// Bob: Account found, but no public key
	// Charlie: Valid account and public key
	participantDetails := map[string]string{
		aliceAccAddrStr:   "nil", // Simulate GetAccount returns nil
		bobAccAddrStr:     "",    // Simulate account with no pubkey
		charlieAccAddrStr: charlieSecp256k1PubHex,
	}
	expectedPubKeysMap := setupMockAccountExpectations(t, mockAccountKeeper, participantDetails)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Store participant entries in inference keeper (WorkerPublicKey is ignored)
	storedParticipants := []*types.Participant{
		{Index: aliceAccAddrStr, Address: aliceAccAddrStr, Weight: 30, Status: types.ParticipantStatus_ACTIVE},
		{Index: bobAccAddrStr, Address: bobAccAddrStr, Weight: 30, Status: types.ParticipantStatus_ACTIVE},
		{Index: charlieAccAddrStr, Address: charlieAccAddrStr, Weight: 40, Status: types.ParticipantStatus_ACTIVE},
	}
	for _, p := range storedParticipants {
		k.SetParticipant(ctx, *p)
	}

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 30, CapWeight: 30},
		{Index: bobAccAddrStr, Weight: 30, CapWeight: 30},
		{Index: charlieAccAddrStr, Weight: 40, CapWeight: 40},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(3)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err, "DKG should proceed if at least one participant is valid")
	require.Len(t, epochBLSData.Participants, 1, "Only Charlie should be included")
	require.Equal(t, charlieAccAddrStr, epochBLSData.Participants[0].Address)
	require.Equal(t, expectedPubKeysMap[charlieAccAddrStr], epochBLSData.Participants[0].Secp256K1PublicKey)
	require.True(t, epochBLSData.Participants[0].PercentageWeight.Equal(math.LegacyNewDec(40)),
		"without guardian reservation, preserve the pre-filter percentage")
}

func TestBLSGuardianEnhancementRunsAfterParticipantFiltering(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	setupMockAccountExpectations(t, mockAccountKeeper, map[string]string{
		aliceAccAddrStr:   aliceSecp256k1PubHex,
		bobAccAddrStr:     "nil",
		charlieAccAddrStr: charlieSecp256k1PubHex,
	})

	aliceAccount, err := sdk.AccAddressFromBech32(aliceAccAddrStr)
	require.NoError(t, err)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.GenesisGuardianParams = &types.GenesisGuardianParams{
		NetworkMaturityThreshold: 10_000,
		NetworkMaturityMinHeight: 0,
		GuardianAddresses:        []string{sdk.ValAddress(aliceAccount).String()},
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &types.GenesisOnlyParams{
		TotalSupply:               1_000_000_000,
		OriginatorSupply:          160_000_000,
		PreProgrammedSaleAmount:   120_000_000,
		SupplyDenom:               "gonka",
		GenesisGuardianMultiplier: types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:    true,
	}))

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 10, CapWeight: 10},
		{Index: bobAccAddrStr, Weight: 100, CapWeight: 100},
		{Index: charlieAccAddrStr, Weight: 100, CapWeight: 100},
	}

	appModule := inference.NewAppModule(codec.NewProtoCodec(codectypes.NewInterfaceRegistry()), k, mockAccountKeeper, nil, nil, nil)
	const epochID = uint64(22)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, epochBLSData.Participants, 2)

	byAddress := make(map[string]blstypes.BLSParticipantInfo, len(epochBLSData.Participants))
	for _, participant := range epochBLSData.Participants {
		byAddress[participant.Address] = participant
	}
	_, bobIncluded := byAddress[bobAccAddrStr]
	require.False(t, bobIncluded)

	guardian, guardianIncluded := byAddress[aliceAccAddrStr]
	require.True(t, guardianIncluded)
	guardianSlots := guardian.SlotEndIndex - guardian.SlotStartIndex + 1
	require.Greater(t, guardianSlots*100, epochBLSData.ITotalSlots*30,
		"guardian enhancement must remain active after filtering")
	require.Less(t, guardianSlots*100, epochBLSData.ITotalSlots*40,
		"guardian reservation must be recomputed from the surviving trust vector")
}

func TestBLSKeyGenerationUsesAccountPubKeyOverWorkerOrValidatorKey(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	// AccountKeeper will provide the source of truth for Alice's PubKey
	participantDetails := map[string]string{
		aliceAccAddrStr: aliceSecp256k1PubHex, // This is the key that MUST be used
	}
	expectedPubKeysMap := setupMockAccountExpectations(t, mockAccountKeeper, participantDetails)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Participant store has different keys for WorkerPublicKey and a (mocked) ValidatorKey string.
	// These should be ignored.
	storedParticipant := types.Participant{
		Index:           aliceAccAddrStr,
		Address:         aliceAccAddrStr,
		ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("some_other_validator_key_data")),
		WorkerPublicKey: base64.StdEncoding.EncodeToString(newSecp256k1PubKeyFromHexStr(t, aliceOtherSecp256k1PubHex).Bytes()), // A different, valid secp256k1 key
		Weight:          100,
		Status:          types.ParticipantStatus_ACTIVE,
	}
	k.SetParticipant(ctx, storedParticipant)

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 100, CapWeight: 100},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(4)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, epochBLSData.Participants, 1)
	blsP := epochBLSData.Participants[0]
	require.Equal(t, aliceAccAddrStr, blsP.Address)
	// Check it used the key from AccountKeeper
	require.Equal(t, expectedPubKeysMap[aliceAccAddrStr], blsP.Secp256K1PublicKey)
	// Check it did NOT use the WorkerPublicKey from the store
	require.NotEqual(t, storedParticipant.WorkerPublicKey, base64.StdEncoding.EncodeToString(blsP.Secp256K1PublicKey))
}

func TestBLSKeyGenerationWithMissingParticipantsInStore(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	// Generate a valid missing address
	missingAddr := generateValidBech32Address(t, "03a34b99f22c790c4e36b2b3c2c35a36db06226e41c692fc82b8b56ac1c540c5bd")
	// AccountKeeper will also return nil for this address, reinforcing that it's fully missing
	participantDetails := map[string]string{
		missingAddr: "nil",
	}
	_ = setupMockAccountExpectations(t, mockAccountKeeper, participantDetails) // Setup expectation for GetAccount to return nil

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)

	// ActiveParticipant is listed, but NO corresponding entry via k.SetParticipant()
	activeParticipants := []*types.ActiveParticipant{
		{Index: missingAddr, Weight: 100, CapWeight: 100},
	}

	epochID := uint64(5)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	_, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.Error(t, err, "EpochBLSData should not be created if participant is not in store, even if in active list")
}

func TestBLSKeyGenerationWithInvalidStoredWorkerKeyAndNoAccountKey(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	allowEmptyBLSGrantQueries(mocks)

	// Generate a valid problem address
	problemAddr := generateValidBech32Address(t, "0365cdf48e56aa2a8c2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a")
	// AccountKeeper will return nil for this address (no account / no pubkey)
	participantDetails := map[string]string{
		problemAddr: "nil",
	}
	_ = setupMockAccountExpectations(t, mockAccountKeeper, participantDetails)

	// Participant IS in the store, but its WorkerPublicKey is malformed.
	// This tests if any old logic might try to fall back to this malformed key if AccountKeeper fails.
	storedParticipantWithBadWKey := types.Participant{
		Index:           problemAddr,
		Address:         problemAddr,
		ValidatorKey:    "valKeyIgnored",
		WorkerPublicKey: "!@#this_is_not_base64_encoded_!@#",
		Weight:          100,
		Status:          types.ParticipantStatus_ACTIVE,
	}
	k.SetParticipant(ctx, storedParticipantWithBadWKey)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)

	activeParticipants := []*types.ActiveParticipant{
		{Index: problemAddr, Weight: 100, CapWeight: 100},
	}

	epochID := uint64(6)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	_, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.Error(t, err, "EpochBLSData should not be created if AccountKeeper yields no key AND stored WorkerKey is invalid")
}

// Note: The actual implementation of initiateBLSKeyGeneration in the inference module (appModule.go or keeper/dkg_initiation.go)
// needs to be updated to use the AccountKeeper to fetch the PubKey for each participant.
// These tests are designed to verify that behavior once implemented.

func TestBLSIntegrationAllowsConcurrentDKG(t *testing.T) {
	setupSDKConfig() // Ensure SDK is configured for address generation
	k, ctx := setupTestKeeperWithBLS(t)
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Set up test accounts
	alicePrivKey := secp256k1.GenPrivKey()
	bobPrivKey := secp256k1.GenPrivKey()
	aliceAccAddr := sdk.AccAddress(alicePrivKey.PubKey().Address())
	bobAccAddr := sdk.AccAddress(bobPrivKey.PubKey().Address())
	aliceAccAddrStr := aliceAccAddr.String()
	bobAccAddrStr := bobAccAddr.String()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)

	// Set up mock expectations for AccountKeeper
	participantDetails := map[string]string{
		aliceAccAddrStr: hex.EncodeToString(alicePrivKey.PubKey().Bytes()),
		bobAccAddrStr:   hex.EncodeToString(bobPrivKey.PubKey().Bytes()),
	}
	expectedPubKeysMap := setupMockAccountExpectations(t, mockAccountKeeper, participantDetails)

	// Verify that the mock setup worked correctly
	require.Len(t, expectedPubKeysMap, 2, "Should have pub keys for both participants")

	// Set up active participants for epoch 1
	epoch1Participants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 50, CapWeight: 50},
		{Index: bobAccAddrStr, Weight: 50, CapWeight: 50},
	}

	// Set up active participants for epoch 2
	epoch2Participants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 60, CapWeight: 60},
		{Index: bobAccAddrStr, Weight: 40, CapWeight: 40},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)

	// Initiate DKG for epoch 1 - should succeed
	epochID1 := uint64(1)
	appModule.InitiateBLSKeyGeneration(ctx, epochID1, epoch1Participants)

	// Verify epoch 1 DKG was initiated
	epochBLSData1, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID1)
	require.NoError(t, err, "Epoch 1 DKG should be initiated")
	require.Equal(t, epochID1, epochBLSData1.EpochId)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData1.DkgPhase)

	// Verify epoch 1 is set as active
	activeEpochID, foundActive := k.BlsKeeper.GetActiveEpochID(ctx)
	require.True(t, foundActive, "Active epoch should be found")
	require.Equal(t, epochID1, activeEpochID, "Epoch 1 should be active")

	// Initiate DKG for epoch 2 while epoch 1 is still running - should succeed (concurrent DKG allowed)
	epochID2 := uint64(2)
	appModule.InitiateBLSKeyGeneration(ctx, epochID2, epoch2Participants)

	// Verify epoch 2 DKG was also initiated (concurrent DKG rounds are allowed)
	epochBLSData2, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID2)
	require.NoError(t, err, "Epoch 2 DKG should be initiated even when epoch 1 is still running")
	require.Equal(t, epochID2, epochBLSData2.EpochId)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData2.DkgPhase)

	// Verify both epochs have their own independent DKG data
	require.NotEqual(t, epochBLSData1.EpochId, epochBLSData2.EpochId, "Epochs should have different IDs")

	// Both should be in DEALING phase
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData1.DkgPhase)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData2.DkgPhase)

	// Active epoch tracking should reflect the most recent one (epoch 2)
	activeEpochID, foundActive = k.BlsKeeper.GetActiveEpochID(ctx)
	require.True(t, foundActive, "Active epoch should be found after second initiation")
	require.Equal(t, epochID2, activeEpochID, "Active epoch should be updated to the most recent one")

	// Both epochs should have valid participant data
	require.Len(t, epochBLSData1.Participants, 2, "Epoch 1 should have 2 participants")
	require.Len(t, epochBLSData2.Participants, 2, "Epoch 2 should have 2 participants")
}

func TestBLSKeyGenerationPrunesExcessWarmKeys(t *testing.T) {
	setupSDKConfig()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	alicePrivKey := secp256k1.GenPrivKey()
	aliceAccAddr := sdk.AccAddress(alicePrivKey.PubKey().Address())
	aliceAccAddrStr := aliceAccAddr.String()

	// Setup account mock
	participantDetails := map[string]string{
		aliceAccAddrStr: hex.EncodeToString(alicePrivKey.PubKey().Bytes()),
	}
	_ = setupMockAccountExpectations(t, mocks.AccountKeeper, participantDetails)

	// Mock BLS params to limit maximum additional keys
	blsParams, err := k.BlsKeeper.GetParams(ctx)
	require.NoError(t, err)
	blsParams.ITotalSlots = 4096 // 16384 total shares / 4096 slots = 4 keys/slot => 1 primary + 3 additional
	err = k.BlsKeeper.(blskeeper.Keeper).SetParams(ctx, blsParams)
	require.NoError(t, err)

	numGrants := 5 // We simulate 5 grants, pruning to 3
	grants := make([]*authztypes.GrantAuthorization, numGrants)
	for i := 0; i < numGrants; i++ {
		warmKey := secp256k1.GenPrivKey()
		granteeAddr := sdk.AccAddress(warmKey.PubKey().Address())
		authAny, err := codectypes.NewAnyWithValue(authztypes.NewGenericAuthorization("/inference.bls.MsgSubmitDealerPart"))
		require.NoError(t, err)
		grants[i] = &authztypes.GrantAuthorization{
			Granter:       aliceAccAddrStr,
			Grantee:       granteeAddr.String(),
			Authorization: authAny,
		}
		baseAcc := authtypes.NewBaseAccount(granteeAddr, warmKey.PubKey(), 0, 0)
		mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), granteeAddr).Return(baseAcc).AnyTimes()
	}

	mocks.AuthzKeeper.EXPECT().
		GranterGrants(gomock.Any(), gomock.Any()).
		Return(&authztypes.QueryGranterGrantsResponse{
			Grants: grants,
		}, nil).
		AnyTimes()

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	// Setup participant
	storedParticipant := types.Participant{
		Index:   aliceAccAddrStr,
		Address: aliceAccAddrStr,
		Weight:  100,
		Status:  types.ParticipantStatus_ACTIVE,
	}
	k.SetParticipant(ctx, storedParticipant)

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 100, CapWeight: 100},
	}

	appModule := inference.NewAppModule(cdc, k, mocks.AccountKeeper, nil, nil, nil)
	epochID := uint64(50)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, epochBLSData.Participants, 1)

	// Max allowed should be 3
	require.Len(t, epochBLSData.Participants[0].AllowedSecp256K1PublicKeys, 3)
}

func TestBLSKeyGenerationFiltersOffCurvePrimaryAndWarmKeys(t *testing.T) {
	setupTestAddresses(t)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Off-curve 33-byte compressed key from report: 02 || 31*00 || 05
	offCurveHex := "020000000000000000000000000000000000000000000000000000000000000005"
	offCurvePrimaryAddr := generateValidBech32Address(t, offCurveHex)

	// Alice has a valid primary key; offCurvePrimaryAddr has an off-curve primary key in AccountKeeper.
	participantDetails := map[string]string{
		aliceAccAddrStr:     aliceSecp256k1PubHex,
		offCurvePrimaryAddr: offCurveHex,
	}
	expectedPubKeysMap := setupMockAccountExpectations(t, mocks.AccountKeeper, participantDetails)

	// Configure grants for Alice:
	// 1) valid warm key (charlieSecp256k1PubHex, 0x03 prefix)
	// 2) valid warm key (bobSecp256k1PubHex, 0x02 prefix - sorts before 0x03)
	// 3) duplicate of bobSecp256k1PubHex
	// 4) equal to Alice's primary key
	// 5) off-curve 33-byte key (02 || 31*00 || 05)
	warmValid03Pub := newSecp256k1PubKeyFromHexStr(t, charlieSecp256k1PubHex)
	warmValid02Pub := newSecp256k1PubKeyFromHexStr(t, bobSecp256k1PubHex)
	warmDup02Priv := secp256k1.GenPrivKey() // different grantee address holding same pubkey bytes as bob
	warmPrimaryPub := newSecp256k1PubKeyFromHexStr(t, aliceSecp256k1PubHex)
	warmOffCurvePub := newSecp256k1PubKeyFromHexStr(t, offCurveHex)

	type granteeFixture struct {
		addr   sdk.AccAddress
		pubKey cryptotypes.PubKey
	}
	fixtures := []granteeFixture{
		{addr: sdk.AccAddress(warmValid03Pub.Address()), pubKey: warmValid03Pub},
		{addr: sdk.AccAddress(warmValid02Pub.Address()), pubKey: warmValid02Pub},
		{addr: sdk.AccAddress(warmDup02Priv.PubKey().Address()), pubKey: warmValid02Pub},
		{addr: sdk.AccAddress([]byte("alice_dup_grantee___")), pubKey: warmPrimaryPub},
		{addr: sdk.AccAddress([]byte("off_curve_grantee___")), pubKey: warmOffCurvePub},
	}

	grants := make([]*authztypes.GrantAuthorization, 0, len(fixtures))
	for _, f := range fixtures {
		authAny, err := codectypes.NewAnyWithValue(authztypes.NewGenericAuthorization("/inference.bls.MsgSubmitDealerPart"))
		require.NoError(t, err)
		grants = append(grants, &authztypes.GrantAuthorization{
			Granter:       aliceAccAddrStr,
			Grantee:       f.addr.String(),
			Authorization: authAny,
		})
		baseAcc := authtypes.NewBaseAccount(f.addr, f.pubKey, 0, 0)
		mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), f.addr).Return(baseAcc).AnyTimes()
	}

	mocks.AuthzKeeper.EXPECT().
		GranterGrants(gomock.Any(), gomock.Any()).
		Return(&authztypes.QueryGranterGrantsResponse{
			Grants: grants,
		}, nil).
		AnyTimes()

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	for _, addrStr := range []string{aliceAccAddrStr, offCurvePrimaryAddr} {
		k.SetParticipant(ctx, types.Participant{
			Index:   addrStr,
			Address: addrStr,
			Weight:  50,
			Status:  types.ParticipantStatus_ACTIVE,
		})
	}

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 50, CapWeight: 50},
		{Index: offCurvePrimaryAddr, Weight: 50, CapWeight: 50},
	}

	appModule := inference.NewAppModule(cdc, k, mocks.AccountKeeper, nil, nil, nil)
	const epochID = uint64(51)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, epochBLSData.Participants, 1, "participant with off-curve primary key must be excluded")

	aliceBLS := epochBLSData.Participants[0]
	require.Equal(t, aliceAccAddrStr, aliceBLS.Address)
	require.Equal(t, expectedPubKeysMap[aliceAccAddrStr], aliceBLS.Secp256K1PublicKey)
	require.Equal(t, [][]byte{
		warmValid02Pub.Bytes(),
		warmValid03Pub.Bytes(),
	}, aliceBLS.AllowedSecp256K1PublicKeys, "only valid unique non-primary warm keys must be retained in sorted order")
}

func TestBLSKeyGeneration_EndToEndOffCurveRegression(t *testing.T) {
	setupTestAddresses(t)
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ctx = ctx.WithChainID("e2e-offcurve-regression-chain").WithBlockHeight(100)

	offCurveHex := "020000000000000000000000000000000000000000000000000000000000000005"
	offCurveBytes, err := hex.DecodeString(offCurveHex)
	require.NoError(t, err)
	offCurvePubKey := &secp256k1.PubKey{Key: offCurveBytes}
	offCurveAddr := sdk.AccAddress(offCurvePubKey.Address()).String()

	// 1. Attempt MsgSubmitNewUnfundedParticipant with the off-curve vector and assert failure.
	inferenceMsgServer := keeper.NewMsgServerImpl(k)
	unfundedMsg := &types.MsgSubmitNewUnfundedParticipant{
		Address:      offCurveAddr,
		Url:          "https://offcurve.example.com",
		ValidatorKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		WorkerKey:    base64.StdEncoding.EncodeToString(make([]byte, 32)),
		PubKey:       base64.StdEncoding.EncodeToString(offCurveBytes),
	}
	require.Error(t, unfundedMsg.ValidateBasic())
	_, err = inferenceMsgServer.SubmitNewUnfundedParticipant(ctx, unfundedMsg)
	require.Error(t, err)
	_, foundOffCurveParticipant := k.GetParticipant(ctx, offCurveAddr)
	require.False(t, foundOffCurveParticipant, "off-curve participant must not be stored")

	// 2. Register valid participants (Alice and Bob) + a corrupted state entry for offCurveAddr,
	//    and configure Alice with 1 valid warm key (Charlie) and 1 off-curve warm key.
	participantDetails := map[string]string{
		aliceAccAddrStr: aliceSecp256k1PubHex,
		bobAccAddrStr:   bobSecp256k1PubHex,
		offCurveAddr:    offCurveHex,
	}
	expectedPubKeysMap := setupMockAccountExpectations(t, mocks.AccountKeeper, participantDetails)

	charlieWarmPub := newSecp256k1PubKeyFromHexStr(t, charlieSecp256k1PubHex)
	charlieWarmAddr := sdk.AccAddress(charlieWarmPub.Address())
	offCurveWarmAddr := sdk.AccAddress([]byte("offcurve_warm_grnt__"))

	authAny, err := codectypes.NewAnyWithValue(authztypes.NewGenericAuthorization("/inference.bls.MsgSubmitDealerPart"))
	require.NoError(t, err)
	aliceGrants := []*authztypes.GrantAuthorization{
		{Granter: aliceAccAddrStr, Grantee: charlieWarmAddr.String(), Authorization: authAny},
		{Granter: aliceAccAddrStr, Grantee: offCurveWarmAddr.String(), Authorization: authAny},
	}
	mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), charlieWarmAddr).
		Return(authtypes.NewBaseAccount(charlieWarmAddr, charlieWarmPub, 0, 0)).
		AnyTimes()
	mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), offCurveWarmAddr).
		Return(authtypes.NewBaseAccount(offCurveWarmAddr, offCurvePubKey, 0, 0)).
		AnyTimes()

	mocks.AuthzKeeper.EXPECT().
		GranterGrants(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ interface{}, req *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error) {
			if req.Granter == aliceAccAddrStr {
				return &authztypes.QueryGranterGrantsResponse{Grants: aliceGrants}, nil
			}
			return &authztypes.QueryGranterGrantsResponse{}, nil
		}).
		AnyTimes()

	for _, addrStr := range []string{aliceAccAddrStr, bobAccAddrStr, offCurveAddr} {
		k.SetParticipant(ctx, types.Participant{
			Index:   addrStr,
			Address: addrStr,
			Weight:  50,
			Status:  types.ParticipantStatus_ACTIVE,
		})
	}

	// Configure compact BLS params for fast end-to-end cryptographic DKG + group key validation.
	blsK := k.BlsKeeper.(blskeeper.Keeper)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(t, err)
	blsParams.ITotalSlots = 10
	blsParams.TSlotsDegreeOffset = 5
	blsParams.DealingPhaseDurationBlocks = 5
	blsParams.VerificationPhaseDurationBlocks = 5
	blsParams.DisputePhaseDurationBlocks = 5
	require.NoError(t, blsK.SetParams(ctx, blsParams))

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := inference.NewAppModule(cdc, k, mocks.AccountKeeper, nil, nil, nil)
	blsMsgServer := blskeeper.NewMsgServerImpl(blsK)

	activeParticipants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 50, CapWeight: 50},
		{Index: bobAccAddrStr, Weight: 50, CapWeight: 50},
		{Index: offCurveAddr, Weight: 50, CapWeight: 50},
	}

	// Helper that runs one full DKG round (Initiate -> Dealing -> Verifying -> Disputing -> Completed)
	// and returns the completed EpochBLSData along with the summed polynomial coefficients.
	runFullDKGRound := func(epochID uint64, seedOffset uint64) (sdk.Context, blstypes.EpochBLSData, []fr.Element) {
		ctx = ctx.WithEventManager(sdk.NewEventManager())
		appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

		// Verify EventKeyGenerationInitiated was emitted and EpochBLSData contains only valid secp256k1 keys.
		require.True(t, hasEventType(ctx, "inference.bls.EventKeyGenerationInitiated"))
		epochData, err := blsK.GetEpochBLSData(ctx, epochID)
		require.NoError(t, err)
		require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochData.DkgPhase)
		require.Len(t, epochData.Participants, 2, "only Alice and Bob must be in the BLS snapshot")

		byAddr := make(map[string]blstypes.BLSParticipantInfo, len(epochData.Participants))
		for _, p := range epochData.Participants {
			require.NoError(t, blstypes.ValidateCompressedSecp256k1PubKey(p.Secp256K1PublicKey))
			for _, warmKey := range p.AllowedSecp256K1PublicKeys {
				require.NoError(t, blstypes.ValidateCompressedSecp256k1PubKey(warmKey))
			}
			byAddr[p.Address] = p
		}
		require.Equal(t, expectedPubKeysMap[aliceAccAddrStr], byAddr[aliceAccAddrStr].Secp256K1PublicKey)
		require.Equal(t, [][]byte{charlieWarmPub.Bytes()}, byAddr[aliceAccAddrStr].AllowedSecp256K1PublicKeys)
		require.Equal(t, expectedPubKeysMap[bobAccAddrStr], byAddr[bobAccAddrStr].Secp256K1PublicKey)
		require.Empty(t, byAddr[bobAccAddrStr].AllowedSecp256K1PublicKeys)

		// Generate polynomial commitments and submit dealer parts for all participants.
		commitmentCount := int(epochData.TSlotsDegree) + 1
		_, _, _, g2Gen := bls12381.Generators()
		dealerCoeffs := make([][]fr.Element, len(epochData.Participants))
		totalCoeffs := make([]fr.Element, commitmentCount)

		for dealerIdx, dealer := range epochData.Participants {
			dealerCoeffs[dealerIdx] = make([]fr.Element, commitmentCount)
			commitments := make([][]byte, commitmentCount)
			for cIdx := 0; cIdx < commitmentCount; cIdx++ {
				var coeff fr.Element
				coeff.SetUint64(seedOffset + uint64(dealerIdx*commitmentCount+cIdx+1))
				dealerCoeffs[dealerIdx][cIdx] = coeff
				totalCoeffs[cIdx].Add(&totalCoeffs[cIdx], &coeff)

				var g2Point bls12381.G2Affine
				g2Point.ScalarMultiplication(&g2Gen, coeff.BigInt(new(big.Int)))
				g2Bytes := g2Point.Bytes()
				commitments[cIdx] = g2Bytes[:]
			}

			encryptedShares := make([]blstypes.EncryptedSharesForParticipant, len(epochData.Participants))
			for recipIdx, recip := range epochData.Participants {
				slotCount := int(recip.SlotEndIndex - recip.SlotStartIndex + 1)
				keysPerSlot := 1 + len(recip.AllowedSecp256K1PublicKeys)
				ciphertexts := make([][]byte, slotCount*keysPerSlot)
				for sIdx := range ciphertexts {
					ct := make([]byte, blstypes.HonestEncryptedShareCiphertextLen)
					ct[0] = 0x04
					ct[1] = byte(dealerIdx)
					ct[2] = byte(recipIdx)
					ct[3] = byte(sIdx)
					ciphertexts[sIdx] = ct
				}
				encryptedShares[recipIdx] = blstypes.EncryptedSharesForParticipant{
					EncryptedShares: ciphertexts,
				}
			}

			_, err := blsMsgServer.SubmitDealerPart(sdk.WrapSDKContext(ctx), &blstypes.MsgSubmitDealerPart{
				Creator:                        dealer.Address,
				EpochId:                        epochID,
				Commitments:                    commitments,
				EncryptedSharesForParticipants: encryptedShares,
			})
			require.NoError(t, err)
		}

		// Transition DEALING -> VERIFYING
		ctx = ctx.WithBlockHeight(epochData.DealingPhaseDeadlineBlock)
		require.NoError(t, blsK.ProcessDKGPhaseTransitionForEpoch(ctx, epochID))
		epochData, err = blsK.GetEpochBLSData(ctx, epochID)
		require.NoError(t, err)
		require.Equal(t, blstypes.DKGPhase_DKG_PHASE_VERIFYING, epochData.DkgPhase)

		// Submit verification vectors (with dealer validity proofs) for all participants
		for verifierIdx, verifier := range epochData.Participants {
			var proofs []blstypes.DealerValidityProof
			for dealerIdx := range epochData.Participants {
				if dealerIdx == verifierIdx {
					continue
				}
				proofHash := blstypes.BuildDealerValidityProofHash(epochID, uint32(dealerIdx))
				proofG1 := hashToG1ForTest(t, proofHash)
				var proofSig []byte
				for slot := verifier.SlotStartIndex; slot <= verifier.SlotEndIndex; slot++ {
					var x fr.Element
					x.SetUint64(uint64(slot + 1))
					shareScalar := evalPolyForTest(dealerCoeffs[dealerIdx], x)
					var sig bls12381.G1Affine
					sig.ScalarMultiplication(&proofG1, shareScalar.BigInt(new(big.Int)))
					sigBytes := sig.Bytes()
					proofSig = append(proofSig, sigBytes[:]...)
				}
				proofs = append(proofs, blstypes.DealerValidityProof{
					DealerIndex:    uint32(dealerIdx),
					ProofSignature: proofSig,
				})
			}

			_, err := blsMsgServer.SubmitVerificationVector(sdk.WrapSDKContext(ctx), &blstypes.MsgSubmitVerificationVector{
				Creator:              verifier.Address,
				EpochId:              epochID,
				DealerValidity:       []bool{true, true},
				DealerValidityProofs: proofs,
			})
			require.NoError(t, err)
		}

		// Transition VERIFYING -> DISPUTING
		ctx = ctx.WithBlockHeight(epochData.VerifyingPhaseDeadlineBlock)
		require.NoError(t, blsK.ProcessDKGPhaseTransitionForEpoch(ctx, epochID))
		epochData, err = blsK.GetEpochBLSData(ctx, epochID)
		require.NoError(t, err)
		require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DISPUTING, epochData.DkgPhase)

		// Transition DISPUTING -> COMPLETED
		ctx = ctx.WithBlockHeight(epochData.DisputingPhaseDeadlineBlock)
		require.NoError(t, blsK.ProcessDKGPhaseTransitionForEpoch(ctx, epochID))
		epochData, err = blsK.GetEpochBLSData(ctx, epochID)
		require.NoError(t, err)
		require.Equal(t, blstypes.DKGPhase_DKG_PHASE_COMPLETED, epochData.DkgPhase)
		require.Len(t, epochData.GroupPublicKey, 96)
		require.Len(t, epochData.SlotPublicKeys, int(epochData.ITotalSlots))

		return ctx, epochData, totalCoeffs
	}

	// 3 & 4. Run Epoch 1 DKG to COMPLETED.
	var epoch1Data blstypes.EpochBLSData
	var epoch1TotalCoeffs []fr.Element
	ctx, epoch1Data, epoch1TotalCoeffs = runFullDKGRound(1, 10)

	// 5. Run Epoch 2 DKG to COMPLETED, then validate Epoch 2's group key using Epoch 1's slot shares -> SIGNED.
	var epoch2Data blstypes.EpochBLSData
	ctx, epoch2Data, _ = runFullDKGRound(2, 100)

	msgHash := computeGroupValidationMsgHashForTest(t, ctx.ChainID(), blsK, epoch2Data.GroupPublicKey, epoch1Data.EpochId)
	msgG1 := hashToG1ForTest(t, msgHash)

	for _, participant := range epoch1Data.Participants {
		var slotIndices []uint32
		var sigPayload []byte
		for slot := participant.SlotStartIndex; slot <= participant.SlotEndIndex; slot++ {
			slotIndices = append(slotIndices, slot)
			var x fr.Element
			x.SetUint64(uint64(slot + 1))
			slotScalar := evalPolyForTest(epoch1TotalCoeffs, x)

			var sig bls12381.G1Affine
			sig.ScalarMultiplication(&msgG1, slotScalar.BigInt(new(big.Int)))
			sigBytes := sig.Bytes()
			sigPayload = append(sigPayload, sigBytes[:]...)
		}

		_, err := blsMsgServer.SubmitGroupKeyValidationSignature(sdk.WrapSDKContext(ctx), &blstypes.MsgSubmitGroupKeyValidationSignature{
			Creator:          participant.Address,
			NewEpochId:       epoch2Data.EpochId,
			SlotIndices:      slotIndices,
			PartialSignature: sigPayload,
		})
		require.NoError(t, err)
	}

	finalEpoch2Data, err := blsK.GetEpochBLSData(ctx, epoch2Data.EpochId)
	require.NoError(t, err)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_SIGNED, finalEpoch2Data.DkgPhase)
	require.NotEmpty(t, finalEpoch2Data.ValidationSignature)
}

func computeGroupValidationMsgHashForTest(t *testing.T, chainID string, blsK blskeeper.Keeper, groupPublicKey []byte, previousEpochID uint64) []byte {
	t.Helper()
	g2Uncompressed256, err := blsK.DecompressG2To256Blst(groupPublicKey)
	require.NoError(t, err)

	gonkaIDHash := sha256.Sum256([]byte(chainID))
	previousEpochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(previousEpochBytes, previousEpochID)

	var encoded []byte
	encoded = append(encoded, previousEpochBytes...)
	encoded = append(encoded, gonkaIDHash[:]...)
	encoded = append(encoded, g2Uncompressed256...)

	h := sha3.NewLegacyKeccak256()
	h.Write(encoded)
	return h.Sum(nil)
}

func hashToG1ForTest(t *testing.T, hash []byte) bls12381.G1Affine {
	t.Helper()
	require.Len(t, hash, 32)
	var be [48]byte
	copy(be[48-32:], hash)
	var u fp.Element
	u.SetBytes(be[:])
	p := bls12381.MapToCurve1(&u)
	hashToCurve.G1Isogeny(&p.X, &p.Y)
	var out bls12381.G1Affine
	out.ClearCofactor(&p)
	return out
}

func evalPolyForTest(coeffs []fr.Element, x fr.Element) fr.Element {
	var result fr.Element
	result.SetZero()
	var power fr.Element
	power.SetOne()
	for i := range coeffs {
		var term fr.Element
		term.Mul(&coeffs[i], &power)
		result.Add(&result, &term)
		power.Mul(&power, &x)
	}
	return result
}

func hasEventType(ctx sdk.Context, eventType string) bool {
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == eventType {
			return true
		}
	}
	return false
}

func setupSnapshotValidationBenchmarkFixture(tb testing.TB, numParticipants, warmKeysPerParticipant int) (inference.AppModule, keeper.Keeper, sdk.Context, []*types.ActiveParticipant) {
	setupSDKConfig()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(tb)

	blsK := k.BlsKeeper.(blskeeper.Keeper)
	blsParams, err := blsK.GetParams(ctx)
	require.NoError(tb, err)
	blsParams.ITotalSlots = 1000 // 16384 / 1000 = 16 total keys/slot => up to 15 warm keys per participant
	require.NoError(tb, blsK.SetParams(ctx, blsParams))

	authAny, err := codectypes.NewAnyWithValue(authztypes.NewGenericAuthorization("/inference.bls.MsgSubmitDealerPart"))
	require.NoError(tb, err)

	grantsByGranter := make(map[string][]*authztypes.GrantAuthorization, numParticipants)
	activeParticipants := make([]*types.ActiveParticipant, numParticipants)

	for i := 0; i < numParticipants; i++ {
		primaryKey := secp256k1.GenPrivKey()
		primaryAddr := sdk.AccAddress(primaryKey.PubKey().Address())
		primaryAddrStr := primaryAddr.String()

		primaryAcc := authtypes.NewBaseAccount(primaryAddr, primaryKey.PubKey(), uint64(i+1), 0)
		mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), primaryAddr).Return(primaryAcc).AnyTimes()

		grants := make([]*authztypes.GrantAuthorization, warmKeysPerParticipant)
		for w := 0; w < warmKeysPerParticipant; w++ {
			warmKey := secp256k1.GenPrivKey()
			warmAddr := sdk.AccAddress(warmKey.PubKey().Address())
			warmAcc := authtypes.NewBaseAccount(warmAddr, warmKey.PubKey(), uint64(10000+i*100+w), 0)
			mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), warmAddr).Return(warmAcc).AnyTimes()
			grants[w] = &authztypes.GrantAuthorization{
				Granter:       primaryAddrStr,
				Grantee:       warmAddr.String(),
				Authorization: authAny,
			}
		}
		grantsByGranter[primaryAddrStr] = grants

		k.SetParticipant(ctx, types.Participant{
			Index:   primaryAddrStr,
			Address: primaryAddrStr,
			Weight:  10,
			Status:  types.ParticipantStatus_ACTIVE,
		})
		activeParticipants[i] = &types.ActiveParticipant{
			Index:     primaryAddrStr,
			Weight:    10,
			CapWeight: 10,
		}
	}

	mocks.AuthzKeeper.EXPECT().
		GranterGrants(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ interface{}, req *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error) {
			return &authztypes.QueryGranterGrantsResponse{Grants: grantsByGranter[req.Granter]}, nil
		}).
		AnyTimes()

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := inference.NewAppModule(cdc, k, mocks.AccountKeeper, nil, nil, nil)
	return appModule, k, ctx, activeParticipants
}

func TestInitiateBLSKeyGeneration_SnapshotValidationTiming(t *testing.T) {
	const (
		numParticipants        = 100
		warmKeysPerParticipant = 15 // maximum allowed for ITotalSlots=1000 (16384/1000 - 1)
	)
	appModule, k, ctx, activeParticipants := setupSnapshotValidationBenchmarkFixture(t, numParticipants, warmKeysPerParticipant)

	start := time.Now()
	const epochID = uint64(77)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)
	fullElapsed := time.Since(start)

	epochBLSData, err := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Len(t, epochBLSData.Participants, numParticipants)

	finalized := make([]blstypes.ParticipantWithWeightAndKey, numParticipants)
	for i, p := range epochBLSData.Participants {
		require.Len(t, p.AllowedSecp256K1PublicKeys, warmKeysPerParticipant)
		finalized[i] = blstypes.ParticipantWithWeightAndKey{
			Address:                    p.Address,
			PercentageWeight:           p.PercentageWeight,
			Secp256k1PublicKey:         p.Secp256K1PublicKey,
			AllowedSecp256k1PublicKeys: p.AllowedSecp256K1PublicKeys,
		}
	}

	blsStart := time.Now()
	require.NoError(t, k.BlsKeeper.InitiateKeyGenerationForEpoch(ctx, epochID+1, finalized))
	blsElapsed := time.Since(blsStart)

	t.Logf("Snapshot validation timing (%d participants x (1 primary + %d warm keys) = %d compressed secp256k1 keys): InitiateKeyGenerationForEpoch=%s, full InitiateBLSKeyGeneration=%s",
		numParticipants, warmKeysPerParticipant, numParticipants*(1+warmKeysPerParticipant), blsElapsed, fullElapsed)
	require.Less(t, blsElapsed, 250*time.Millisecond, "x/bls snapshot secp256k1 point validation should remain negligible relative to EndBlock")
	require.Less(t, fullElapsed, 2*time.Second, "EndBlock BLS snapshot construction + validation should remain well within block budget")
}

func BenchmarkInitiateBLSKeyGenerationSnapshotValidation(b *testing.B) {
	const (
		numParticipants        = 100
		warmKeysPerParticipant = 15
	)
	appModule, _, baseCtx, activeParticipants := setupSnapshotValidationBenchmarkFixture(b, numParticipants, warmKeysPerParticipant)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		epochID := uint64(1000 + i)
		appModule.InitiateBLSKeyGeneration(baseCtx, epochID, activeParticipants)
	}
}



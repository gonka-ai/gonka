package inference_test

import (
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

// setupTestKeeperWithBLS creates a test keeper with BLS integration
func setupTestKeeperWithBLS(t testing.TB) (keeper.Keeper, sdk.Context) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	return k, ctx
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
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

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
		{Index: aliceAccAddrStr, Weight: 50},
		{Index: bobAccAddrStr, Weight: 30},
		{Index: charlieAccAddrStr, Weight: 20},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(1)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
	require.Len(t, epochBLSData.Participants, 3)
	for _, p := range epochBLSData.Participants {
		expectedBytes, ok := expectedPubKeysMap[p.Address]
		require.True(t, ok)
		require.Equal(t, expectedBytes, p.Secp256K1PublicKey)
	}
}

func TestBLSKeyGenerationWithEmptyParticipants(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)

	epochID := uint64(2)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, []*types.ActiveParticipant{})

	_, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found)
}

func TestBLSKeyGenerationWithAccountKeyIssues(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

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
		{Index: aliceAccAddrStr, Weight: 30},
		{Index: bobAccAddrStr, Weight: 30},
		{Index: charlieAccAddrStr, Weight: 40},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(3)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.True(t, found, "DKG should proceed if at least one participant is valid")
	require.Len(t, epochBLSData.Participants, 1, "Only Charlie should be included")
	require.Equal(t, charlieAccAddrStr, epochBLSData.Participants[0].Address)
	require.Equal(t, expectedPubKeysMap[charlieAccAddrStr], epochBLSData.Participants[0].Secp256K1PublicKey)
}

func TestBLSKeyGenerationUsesAccountPubKeyOverWorkerOrValidatorKey(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

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
		{Index: aliceAccAddrStr, Weight: 100},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)
	epochID := uint64(4)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	epochBLSData, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.True(t, found)
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
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

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
		{Index: missingAddr, Weight: 100},
	}

	epochID := uint64(5)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	_, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found, "EpochBLSData should not be created if participant is not in store, even if in active list")
}

func TestBLSKeyGenerationWithInvalidStoredWorkerKeyAndNoAccountKey(t *testing.T) {
	setupTestAddresses(t)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockAccountKeeper := keepertest.NewMockAccountKeeper(ctrl)
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

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
		{Index: problemAddr, Weight: 100},
	}

	epochID := uint64(6)
	appModule.InitiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	_, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found, "EpochBLSData should not be created if AccountKeeper yields no key AND stored WorkerKey is invalid")
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
		{Index: aliceAccAddrStr, Weight: 50},
		{Index: bobAccAddrStr, Weight: 50},
	}

	// Set up active participants for epoch 2
	epoch2Participants := []*types.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 60},
		{Index: bobAccAddrStr, Weight: 40},
	}

	appModule := inference.NewAppModule(cdc, k, mockAccountKeeper, nil, nil, nil)

	// Initiate DKG for epoch 1 - should succeed
	epochID1 := uint64(1)
	appModule.InitiateBLSKeyGeneration(ctx, epochID1, epoch1Participants)

	// Verify epoch 1 DKG was initiated
	epochBLSData1, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID1)
	require.True(t, found, "Epoch 1 DKG should be initiated")
	require.Equal(t, epochID1, epochBLSData1.EpochId)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData1.DkgPhase)

	// Verify epoch 1 is set as active
	activeEpochID, found := k.BlsKeeper.GetActiveEpochID(ctx)
	require.True(t, found, "Active epoch should be found")
	require.Equal(t, epochID1, activeEpochID, "Epoch 1 should be active")

	// Initiate DKG for epoch 2 while epoch 1 is still running - should succeed (concurrent DKG allowed)
	epochID2 := uint64(2)
	appModule.InitiateBLSKeyGeneration(ctx, epochID2, epoch2Participants)

	// Verify epoch 2 DKG was also initiated (concurrent DKG rounds are allowed)
	epochBLSData2, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID2)
	require.True(t, found, "Epoch 2 DKG should be initiated even when epoch 1 is still running")
	require.Equal(t, epochID2, epochBLSData2.EpochId)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData2.DkgPhase)

	// Verify both epochs have their own independent DKG data
	require.NotEqual(t, epochBLSData1.EpochId, epochBLSData2.EpochId, "Epochs should have different IDs")

	// Both should be in DEALING phase
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData1.DkgPhase)
	require.Equal(t, blstypes.DKGPhase_DKG_PHASE_DEALING, epochBLSData2.DkgPhase)

	// Active epoch tracking should reflect the most recent one (epoch 2)
	activeEpochID, found = k.BlsKeeper.GetActiveEpochID(ctx)
	require.True(t, found, "Active epoch should be found after second initiation")
	require.Equal(t, epochID2, activeEpochID, "Active epoch should be updated to the most recent one")

	// Both epochs should have valid participant data
	require.Len(t, epochBLSData1.Participants, 2, "Epoch 1 should have 2 participants")
	require.Len(t, epochBLSData2.Participants, 2, "Epoch 2 should have 2 participants")
}

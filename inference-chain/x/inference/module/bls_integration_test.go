package inference

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
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

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

// init function to generate the addresses after the SDK is properly configured
func init() {
	// Configure the SDK with the gonka prefix
	config := sdk.GetConfig()
	config.SetBech32PrefixForAccount("gonka", "gonkapub")
	config.Seal()
}

// setupTestAddresses generates valid bech32 addresses for testing
func setupTestAddresses(t *testing.T) {
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

	participants := []*inferencetypes.Participant{
		{Index: aliceAccAddrStr, Address: aliceAccAddrStr, ValidatorKey: "valKeyAlice", WorkerPublicKey: "ignoredWKeyAlice", Weight: 50, Status: inferencetypes.ParticipantStatus_ACTIVE},
		{Index: bobAccAddrStr, Address: bobAccAddrStr, ValidatorKey: "valKeyBob", WorkerPublicKey: "ignoredWKeyBob", Weight: 30, Status: inferencetypes.ParticipantStatus_ACTIVE},
		{Index: charlieAccAddrStr, Address: charlieAccAddrStr, ValidatorKey: "valKeyCharlie", WorkerPublicKey: "ignoredWKeyCharlie", Weight: 20, Status: inferencetypes.ParticipantStatus_ACTIVE},
	}
	for _, p := range participants {
		k.SetParticipant(ctx, *p)
	}

	activeParticipants := []*inferencetypes.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 50},
		{Index: bobAccAddrStr, Weight: 30},
		{Index: charlieAccAddrStr, Weight: 20},
	}

	appModule := NewAppModule(cdc, k, mockAccountKeeper, nil, nil)
	epochID := uint64(1)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

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
	appModule := NewAppModule(cdc, k, mockAccountKeeper, nil, nil)

	epochID := uint64(2)
	appModule.initiateBLSKeyGeneration(ctx, epochID, []*inferencetypes.ActiveParticipant{})

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
	storedParticipants := []*inferencetypes.Participant{
		{Index: aliceAccAddrStr, Address: aliceAccAddrStr, Weight: 30, Status: inferencetypes.ParticipantStatus_ACTIVE},
		{Index: bobAccAddrStr, Address: bobAccAddrStr, Weight: 30, Status: inferencetypes.ParticipantStatus_ACTIVE},
		{Index: charlieAccAddrStr, Address: charlieAccAddrStr, Weight: 40, Status: inferencetypes.ParticipantStatus_ACTIVE},
	}
	for _, p := range storedParticipants {
		k.SetParticipant(ctx, *p)
	}

	activeParticipants := []*inferencetypes.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 30},
		{Index: bobAccAddrStr, Weight: 30},
		{Index: charlieAccAddrStr, Weight: 40},
	}

	appModule := NewAppModule(cdc, k, mockAccountKeeper, nil, nil)
	epochID := uint64(3)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

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
	storedParticipant := inferencetypes.Participant{
		Index:           aliceAccAddrStr,
		Address:         aliceAccAddrStr,
		ValidatorKey:    base64.StdEncoding.EncodeToString([]byte("some_other_validator_key_data")),
		WorkerPublicKey: base64.StdEncoding.EncodeToString(newSecp256k1PubKeyFromHexStr(t, aliceOtherSecp256k1PubHex).Bytes()), // A different, valid secp256k1 key
		Weight:          100,
		Status:          inferencetypes.ParticipantStatus_ACTIVE,
	}
	k.SetParticipant(ctx, storedParticipant)

	activeParticipants := []*inferencetypes.ActiveParticipant{
		{Index: aliceAccAddrStr, Weight: 100},
	}

	appModule := NewAppModule(cdc, k, mockAccountKeeper, nil, nil)
	epochID := uint64(4)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

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
	appModule := NewAppModule(cdc, k, mockAccountKeeper, nil, nil)

	// ActiveParticipant is listed, but NO corresponding entry via k.SetParticipant()
	activeParticipants := []*inferencetypes.ActiveParticipant{
		{Index: missingAddr, Weight: 100},
	}

	epochID := uint64(5)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

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
	storedParticipantWithBadWKey := inferencetypes.Participant{
		Index:           problemAddr,
		Address:         problemAddr,
		ValidatorKey:    "valKeyIgnored",
		WorkerPublicKey: "!@#this_is_not_base64_encoded_!@#",
		Weight:          100,
		Status:          inferencetypes.ParticipantStatus_ACTIVE,
	}
	k.SetParticipant(ctx, storedParticipantWithBadWKey)

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	appModule := NewAppModule(cdc, k, mockAccountKeeper, nil, nil)

	activeParticipants := []*inferencetypes.ActiveParticipant{
		{Index: problemAddr, Weight: 100},
	}

	epochID := uint64(6)
	appModule.initiateBLSKeyGeneration(ctx, epochID, activeParticipants)

	_, found := k.BlsKeeper.GetEpochBLSData(ctx, epochID)
	require.False(t, found, "EpochBLSData should not be created if AccountKeeper yields no key AND stored WorkerKey is invalid")
}

// Note: The actual implementation of initiateBLSKeyGeneration in the inference module (appModule.go or keeper/dkg_initiation.go)
// needs to be updated to use the AccountKeeper to fetch the PubKey for each participant.
// These tests are designed to verify that behavior once implemented.

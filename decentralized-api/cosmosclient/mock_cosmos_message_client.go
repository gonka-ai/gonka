package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/protobuf/proto"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	blstypes "github.com/productscience/inference/x/bls/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

type MockCosmosMessageClient struct {
	mock.Mock
	ctx context.Context
}

func (m *MockCosmosMessageClient) GetApiAccount() apiconfig.ApiAccount {
	return apiconfig.ApiAccount{}
}

func (m *MockCosmosMessageClient) Status(ctx context.Context) (*ctypes.ResultStatus, error) {
	args := m.Called(ctx)
	return args.Get(0).(*ctypes.ResultStatus), args.Error(1)
}

func (m *MockCosmosMessageClient) GetAddress() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockCosmosMessageClient) GetContext() context.Context {
	args := m.Called()
	res := args.Get(0)
	if res == nil {
		return context.Background()
	}
	return res.(context.Context)
}

func (m *MockCosmosMessageClient) GetKeyring() *keyring.Keyring {
	args := m.Called()
	return args.Get(0).(*keyring.Keyring)
}

func (m *MockCosmosMessageClient) GetClientContext() sdkclient.Context {
	args := m.Called()
	return args.Get(0).(sdkclient.Context)
}

func (m *MockCosmosMessageClient) GetAccountAddress() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockCosmosMessageClient) GetAccountPubKey() cryptotypes.PubKey {
	args := m.Called()
	return args.Get(0).(cryptotypes.PubKey)
}

func (m *MockCosmosMessageClient) GetSignerAddress() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockCosmosMessageClient) SignBytes(seed []byte) ([]byte, error) {
	args := m.Called(seed)
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockCosmosMessageClient) DecryptBytes(ciphertext []byte) ([]byte, error) {
	args := m.Called(ciphertext)
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockCosmosMessageClient) EncryptBytes(plaintext []byte) ([]byte, error) {
	args := m.Called(plaintext)
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockCosmosMessageClient) StartInference(transaction *inference.MsgStartInference) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) FinishInference(transaction *inference.MsgFinishInference) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) ReportValidation(transaction *inference.MsgValidation) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) ClaimRewards(transaction *inference.MsgClaimRewards) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	args := m.Called(ctx, address)
	return args.Get(0).([]sdk.Coin), args.Error(1)
}

func (m *MockCosmosMessageClient) SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitSeed(transaction *inference.MsgSubmitSeed) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) CreateTrainingTask(transaction *inference.MsgCreateTrainingTask) (*inference.MsgCreateTrainingTaskResponse, error) {
	args := m.Called(transaction)
	return args.Get(0).(*inference.MsgCreateTrainingTaskResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) ClaimTrainingTaskForAssignment(transaction *inference.MsgClaimTrainingTaskForAssignment) (*inference.MsgClaimTrainingTaskForAssignmentResponse, error) {
	args := m.Called(transaction)
	return args.Get(0).(*inference.MsgClaimTrainingTaskForAssignmentResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) AssignTrainingTask(transaction *inference.MsgAssignTrainingTask) (*inference.MsgAssignTrainingTaskResponse, error) {
	args := m.Called(transaction)
	return args.Get(0).(*inference.MsgAssignTrainingTaskResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) BridgeExchange(transaction *types.MsgBridgeExchange) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) GetBridgeAddresses(ctx context.Context, chainId string) ([]types.BridgeContractAddress, error) {
	args := m.Called(ctx, chainId)
	return args.Get(0).([]types.BridgeContractAddress), args.Error(1)
}

func (m *MockCosmosMessageClient) SendTransactionAsyncWithRetry(msg sdk.Msg) (*sdk.TxResponse, error) {
	args := m.Called(msg)
	return args.Get(0).(*sdk.TxResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) SendTransactionAsyncNoRetry(msg sdk.Msg) (*sdk.TxResponse, error) {
	args := m.Called(msg)
	return args.Get(0).(*sdk.TxResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) GetUpgradePlan() (*upgradetypes.QueryCurrentPlanResponse, error) {
	args := m.Called()
	return args.Get(0).(*upgradetypes.QueryCurrentPlanResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) GetPartialUpgrades() (*types.QueryAllPartialUpgradeResponse, error) {
	args := m.Called()
	return args.Get(0).(*types.QueryAllPartialUpgradeResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) NewUpgradeQueryClient() upgradetypes.QueryClient {
	args := m.Called()
	return args.Get(0).(upgradetypes.QueryClient)
}

func (m *MockCosmosMessageClient) NewInferenceQueryClient() types.QueryClient {
	args := m.Called()
	return args.Get(0).(types.QueryClient)
}

func (m *MockCosmosMessageClient) NewCometQueryClient() cmtservice.ServiceClient {
	args := m.Called()
	return args.Get(0).(cmtservice.ServiceClient)
}

func (m *MockCosmosMessageClient) SendTransactionSyncNoRetry(transaction proto.Message, dstMsg proto.Message) error {
	args := m.Called(transaction, dstMsg)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitVerificationVector(transaction *blstypes.MsgSubmitVerificationVector) (*sdk.TxResponse, error) {
	args := m.Called(transaction)
	return args.Get(0).(*sdk.TxResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) SubmitGroupKeyValidationSignature(transaction *blstypes.MsgSubmitGroupKeyValidationSignature) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitPartialSignature(requestId []byte, slotIndices []uint32, partialSignature []byte) error {
	args := m.Called(requestId, slotIndices, partialSignature)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) NewBLSQueryClient() blstypes.QueryClient {
	args := m.Called()
	return args.Get(0).(blstypes.QueryClient)
}

func (m *MockCosmosMessageClient) NewRestrictionsQueryClient() restrictionstypes.QueryClient {
	args := m.Called()
	return args.Get(0).(restrictionstypes.QueryClient)
}

package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/protobuf/proto"
	inferenceapi "github.com/productscience/inference/api/inference/inference"
	inferencetypes "github.com/productscience/inference/x/inference/types"
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

func (m *MockCosmosMessageClient) GetSignerPubKey() cryptotypes.PubKey {
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

func (m *MockCosmosMessageClient) StartInference(transaction *inferenceapi.MsgStartInference) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) FinishInference(transaction *inferenceapi.MsgFinishInference) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) ReportValidation(transaction *inferenceapi.MsgValidation) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitNewUnfundedParticipant(transaction *inferenceapi.MsgSubmitNewUnfundedParticipant) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) ClaimRewards(transaction *inferenceapi.MsgClaimRewards) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	args := m.Called(ctx, address)
	return args.Get(0).([]sdk.Coin), args.Error(1)
}

func (m *MockCosmosMessageClient) SubmitPocValidationsV2(transaction *inferencetypes.MsgSubmitPocValidationsV2) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitPoCV2StoreCommit(transaction *inferencetypes.MsgPoCV2StoreCommit) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitMLNodeWeightDistribution(transaction *inferencetypes.MsgMLNodeWeightDistribution) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitSeed(transaction *inferenceapi.MsgSubmitSeed) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) SubmitUnitOfComputePriceProposal(transaction *inferenceapi.MsgSubmitUnitOfComputePriceProposal) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) BridgeExchange(transaction *inferencetypes.MsgBridgeExchange) error {
	args := m.Called(transaction)
	return args.Error(0)
}

func (m *MockCosmosMessageClient) GetBridgeAddresses(ctx context.Context, chainId string) ([]inferencetypes.BridgeContractAddress, error) {
	args := m.Called(ctx, chainId)
	return args.Get(0).([]inferencetypes.BridgeContractAddress), args.Error(1)
}

func (m *MockCosmosMessageClient) BridgeTransactionsByReceipt(ctx context.Context, originChain, blockNumber, receiptIndex string) ([]inferencetypes.BridgeTransaction, error) {
	args := m.Called(ctx, originChain, blockNumber, receiptIndex)
	var txs []inferencetypes.BridgeTransaction
	if r := args.Get(0); r != nil {
		txs = r.([]inferencetypes.BridgeTransaction)
	}
	return txs, args.Error(1)
}

func (m *MockCosmosMessageClient) SendTransactionAsyncWithRetry(msg sdk.Msg, deadlineBlock ...int64) (*sdk.TxResponse, error) {
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

func (m *MockCosmosMessageClient) GetPartialUpgrades() (*inferencetypes.QueryAllPartialUpgradeResponse, error) {
	args := m.Called()
	return args.Get(0).(*inferencetypes.QueryAllPartialUpgradeResponse), args.Error(1)
}

func (m *MockCosmosMessageClient) NewUpgradeQueryClient() upgradetypes.QueryClient {
	args := m.Called()
	return args.Get(0).(upgradetypes.QueryClient)
}

func (m *MockCosmosMessageClient) NewInferenceQueryClient() inferencetypes.QueryClient {
	args := m.Called()
	return args.Get(0).(inferencetypes.QueryClient)
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

func (m *MockCosmosMessageClient) RespondDealerComplaints(transaction *blstypes.MsgRespondDealerComplaints) error {
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

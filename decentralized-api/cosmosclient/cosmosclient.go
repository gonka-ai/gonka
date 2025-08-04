package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient/tx_manager"
	"decentralized-api/internal/nats/client"
	"decentralized-api/logging"
	"errors"
	"fmt"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"log"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/golang/protobuf/proto"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

type InferenceCosmosClient struct {
	ctx        context.Context
	apiAccount *apiconfig.ApiAccount
	address    string
	manager    tx_manager.TxManager
}

func NewInferenceCosmosClientWithRetry(
	ctx context.Context,
	addressPrefix string,
	maxRetries int,
	delay time.Duration,
	config *apiconfig.ConfigManager) (*InferenceCosmosClient, error) {
	var client *InferenceCosmosClient
	var err error
	logging.Info("Connecting to cosmos sdk node", types.System, "config", config, "height", config.GetHeight())
	for i := 0; i < maxRetries; i++ {
		client, err = NewInferenceCosmosClient(ctx, addressPrefix, config)
		if err == nil {
			return client, nil
		}
		log.Printf("Failed to connect to cosmos sdk node, retrying in %s. err = %s", delay, err)
		time.Sleep(delay)
	}

	return nil, errors.New("failed to connect to cosmos sdk node after multiple retries")
}

func expandPath(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		usr, err := user.Current()
		if err != nil {
			return "", err
		}
		path = filepath.Join(usr.HomeDir, path[2:])
	}
	return filepath.Abs(path)
}

// 'file' keyring backend to automatically provide interactive prompts for signing
func updateKeyringIfNeeded(client *cosmosclient.Client, keyringDir string, config *apiconfig.ConfigManager) error {
	nodeConfig := config.GetChainNodeConfig()
	if nodeConfig.KeyringBackend == keyring.BackendFile {
		interfaceRegistry := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(interfaceRegistry)

		cdc := codec.NewProtoCodec(interfaceRegistry)
		kr, err := keyring.New(
			"inferenced",
			nodeConfig.KeyringBackend,
			keyringDir,
			strings.NewReader(nodeConfig.KeyringPassword),
			cdc,
		)
		if err != nil {
			log.Printf("Error creating keyring: %s", err)
			return err
		}
		client.AccountRegistry.Keyring = kr
		return nil
	}
	return nil
}

func NewInferenceCosmosClient(ctx context.Context, addressPrefix string, config *apiconfig.ConfigManager) (*InferenceCosmosClient, error) {
	nodeConfig := config.GetChainNodeConfig()
	keyringDir, err := expandPath(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	log.Printf("Initializing cosmos Client."+
		"NodeUrl = %s. KeyringBackend = %s. KeyringDir = %s", nodeConfig.Url, nodeConfig.KeyringBackend, keyringDir)
	cosmoclient, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix(addressPrefix),
		cosmosclient.WithKeyringServiceName("inferenced"),
		cosmosclient.WithNodeAddress(nodeConfig.Url),
		cosmosclient.WithKeyringDir(keyringDir),
		cosmosclient.WithGasPrices("0icoin"),
		cosmosclient.WithFees("0icoin"),
		cosmosclient.WithGas("auto"),
		cosmosclient.WithGasAdjustment(5),
	)
	if err != nil {
		log.Printf("Error creating cosmos client: %s", err)
		return nil, err
	}
	err = updateKeyringIfNeeded(&cosmoclient, keyringDir, config)
	if err != nil {
		log.Printf("Error updating keyring: %s", err)
		return nil, err
	}

	apiAccount, err := apiconfig.NewApiAccount(addressPrefix, nodeConfig, &cosmoclient)
	if err != nil {
		log.Printf("Error creating api account: %s", err)
		return nil, err
	}
	accAddress, err := apiAccount.AccountAddressBech32()
	if err != nil {
		log.Printf("Error getting account address: %s", err)
		return nil, err
	}
	log.Printf("Account address: %s", accAddress)

	natsConfig := config.GetNatsConfig()
	natsConn, err := client.ConnectToNats(natsConfig.Host, natsConfig.Port, "tx_manager")
	if err != nil {
		return nil, err
	}

	mn, err := tx_manager.StartTxManager(ctx, &cosmoclient, apiAccount, time.Second*60, natsConn, accAddress)
	if err != nil {
		return nil, err
	}

	return &InferenceCosmosClient{
		ctx:        ctx,
		address:    accAddress,
		apiAccount: apiAccount,
		manager:    mn,
	}, nil
}

type CosmosMessageClient interface {
	SignBytes(seed []byte) ([]byte, error)
	StartInference(transaction *inference.MsgStartInference) error
	FinishInference(transaction *inference.MsgFinishInference) error
	ReportValidation(transaction *inference.MsgValidation) error
	SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error
	SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error
	SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error
	SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error
	SubmitSeed(transaction *inference.MsgSubmitSeed) error
	ClaimRewards(transaction *inference.MsgClaimRewards) error
	CreateTrainingTask(transaction *inference.MsgCreateTrainingTask) (*inference.MsgCreateTrainingTaskResponse, error)
	ClaimTrainingTaskForAssignment(transaction *inference.MsgClaimTrainingTaskForAssignment) (*inference.MsgClaimTrainingTaskForAssignmentResponse, error)
	AssignTrainingTask(transaction *inference.MsgAssignTrainingTask) (*inference.MsgAssignTrainingTaskResponse, error)
	SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error
	BridgeExchange(transaction *types.MsgBridgeExchange) error
	NewInferenceQueryClient() types.QueryClient
	NewCometQueryClient() cmtservice.ServiceClient
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
	SendTransactionAsyncWithRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionSyncNoRetry(transaction proto.Message, dstMsg proto.Message) error
	Status(ctx context.Context) (*ctypes.ResultStatus, error)
	GetContext() context.Context
	GetKeyring() *keyring.Keyring
	GetClientContext() sdkclient.Context
	GetAccountAddress() string
	GetAccountPubKey() cryptotypes.PubKey
	GetSignerAddress() string
	GetAddress() string
	GetApiAccount() apiconfig.ApiAccount
}

func (icc *InferenceCosmosClient) GetApiAccount() apiconfig.ApiAccount {
	return icc.manager.GetApiAccount()
}

func (icc *InferenceCosmosClient) GetClientContext() sdkclient.Context {
	return icc.manager.GetClientContext()
}

func (icc *InferenceCosmosClient) Status(ctx context.Context) (*ctypes.ResultStatus, error) {
	return icc.manager.Status(ctx)
}

func (icc *InferenceCosmosClient) GetContext() context.Context {
	return icc.ctx
}

func (icc *InferenceCosmosClient) GetAddress() string {
	return icc.address
}

func (icc *InferenceCosmosClient) GetKeyring() *keyring.Keyring {
	return icc.manager.GetKeyring()
}

func (icc *InferenceCosmosClient) GetAccountAddress() string {
	address, err := icc.apiAccount.AccountAddressBech32()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "error", err)
		return ""
	}
	return address
}

func (icc *InferenceCosmosClient) GetAccountPubKey() cryptotypes.PubKey {
	return icc.apiAccount.AccountKey
}

func (icc *InferenceCosmosClient) GetSignerAddress() string {
	address, err := icc.apiAccount.SignerAddressBech32()
	if err != nil {
		logging.Error("Failed to get signer address", types.Messages, "error", err)
		return ""
	}
	return address
}

func (icc *InferenceCosmosClient) SignBytes(seed []byte) ([]byte, error) {
	accName := icc.apiAccount.SignerAccount.Name
	kr := *icc.GetKeyring()
	bytes, _, err := kr.Sign(accName, seed, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) StartInference(transaction *inference.MsgStartInference) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.address
	transaction.ExecutedBy = icc.address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inference.MsgValidation) error {
	transaction.Creator = icc.address
	logging.Info("Reporting validation", types.Validation, "value", transaction.Value, "type", fmt.Sprintf("%T", transaction), "creator", transaction.Creator)
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) ClaimRewards(transaction *inference.MsgClaimRewards) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	return icc.manager.BankBalances(ctx, address)
}

func (icc *InferenceCosmosClient) SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitSeed(transaction *inference.MsgSubmitSeed) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error {
	transaction.Creator = icc.address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) CreateTrainingTask(transaction *inference.MsgCreateTrainingTask) (*inference.MsgCreateTrainingTaskResponse, error) {
	transaction.Creator = icc.address
	msg := &inference.MsgCreateTrainingTaskResponse{}

	if err := icc.SendTransactionSyncNoRetry(transaction, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (icc *InferenceCosmosClient) ClaimTrainingTaskForAssignment(transaction *inference.MsgClaimTrainingTaskForAssignment) (*inference.MsgClaimTrainingTaskForAssignmentResponse, error) {
	transaction.Creator = icc.address
	msg := &inference.MsgClaimTrainingTaskForAssignmentResponse{}
	if err := icc.SendTransactionSyncNoRetry(transaction, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (icc *InferenceCosmosClient) AssignTrainingTask(transaction *inference.MsgAssignTrainingTask) (*inference.MsgAssignTrainingTaskResponse, error) {
	transaction.Creator = icc.address
	result, err := icc.manager.SendTransactionSyncNoRetry(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err, "result", result)
		return nil, err
	}

	msg := &inference.MsgAssignTrainingTaskResponse{}
	err = tx_manager.ParseMsgResponse(result.TxResult.Data, 0, msg)
	if err != nil {
		logging.Error("Failed to parse message response", types.Messages, "error", err)
		return nil, err
	}
	return msg, err
}

func (icc *InferenceCosmosClient) BridgeExchange(transaction *types.MsgBridgeExchange) error {
	transaction.Validator = icc.address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SendTransactionAsyncWithRetry(msg sdk.Msg) (*sdk.TxResponse, error) {
	return icc.manager.SendTransactionAsyncWithRetry(msg)
}

func (icc *InferenceCosmosClient) SendTransactionAsyncNoRetry(msg sdk.Msg) (*sdk.TxResponse, error) {
	return icc.manager.SendTransactionAsyncNoRetry(msg)
}

func (icc *InferenceCosmosClient) GetUpgradePlan() (*upgradetypes.QueryCurrentPlanResponse, error) {
	return icc.NewUpgradeQueryClient().CurrentPlan(icc.ctx, &upgradetypes.QueryCurrentPlanRequest{})
}

func (icc *InferenceCosmosClient) GetPartialUpgrades() (*types.QueryAllPartialUpgradeResponse, error) {
	return icc.NewInferenceQueryClient().PartialUpgradeAll(icc.ctx, &types.QueryAllPartialUpgradeRequest{})
}

func (icc *InferenceCosmosClient) NewUpgradeQueryClient() upgradetypes.QueryClient {
	return upgradetypes.NewQueryClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) NewInferenceQueryClient() types.QueryClient {
	return types.NewQueryClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) NewCometQueryClient() cmtservice.ServiceClient {
	return cmtservice.NewServiceClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) SendTransactionSyncNoRetry(transaction proto.Message, dstMsg proto.Message) error {
	result, err := icc.manager.SendTransactionSyncNoRetry(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err, "result", result)
		return err
	}

	err = tx_manager.ParseMsgResponse(result.TxResult.Data, 0, dstMsg)
	if err != nil {
		logging.Error("Failed to parse message response", types.Messages, "error", err)
		return err
	}
	return nil
}

package cosmosclient

import (
	"context"
	"crypto/rand"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient/tx_manager"
	"decentralized-api/internal/nats/client"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"errors"
	"fmt"
	"log"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdkclient "github.com/cosmos/cosmos-sdk/client"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/golang/protobuf/proto"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

type InferenceCosmosClient struct {
	ctx        context.Context
	apiAccount *apiconfig.ApiAccount
	Address    string
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
		cosmosclient.WithGasPrices("0ngonka"),
		cosmosclient.WithFees("0ngonka"),
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
		Address:    accAddress,
		apiAccount: apiAccount,
		manager:    mn,
	}, nil
}

type CosmosMessageClient interface {
	SignBytes(seed []byte) ([]byte, error)
	DecryptBytes(ciphertext []byte) ([]byte, error)
	EncryptBytes(plaintext []byte) ([]byte, error)
	StartInference(transaction *inference.MsgStartInference) error
	FinishInference(transaction *inference.MsgFinishInference) error
	ReportValidation(transaction *inference.MsgValidation) error
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
	GetBridgeAddresses(ctx context.Context, chainId string) ([]types.BridgeContractAddress, error)
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
	SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error
	SubmitVerificationVector(transaction *blstypes.MsgSubmitVerificationVector) (*sdk.TxResponse, error)
	SubmitGroupKeyValidationSignature(transaction *blstypes.MsgSubmitGroupKeyValidationSignature) error
	SubmitPartialSignature(requestId []byte, slotIndices []uint32, partialSignature []byte) error
	NewBLSQueryClient() blstypes.QueryClient
	NewRestrictionsQueryClient() restrictionstypes.QueryClient
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
	return icc.Address
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

func (icc *InferenceCosmosClient) DecryptBytes(ciphertext []byte) ([]byte, error) {
	name := icc.apiAccount.SignerAccount.Name
	// Use the new keyring Decrypt method
	kr := *icc.GetKeyring()
	bytes, err := kr.Decrypt(name, ciphertext, nil, nil)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) EncryptBytes(plaintext []byte) ([]byte, error) {
	name := icc.apiAccount.SignerAccount.Name
	// Use the new keyring Encrypt method with rand.Reader
	kr := *icc.GetKeyring()
	bytes, err := kr.Encrypt(rand.Reader, name, plaintext, nil, nil)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) StartInference(transaction *inference.MsgStartInference) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.Address
	transaction.ExecutedBy = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inference.MsgValidation) error {
	transaction.Creator = icc.Address
	logging.Info("Reporting validation", types.Validation, "value", transaction.Value, "type", fmt.Sprintf("%T", transaction), "creator", transaction.Creator)
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) ClaimRewards(transaction *inference.MsgClaimRewards) error {
	transaction.Creator = icc.Address
	resp, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	logging.Info("Claimed rewards", types.Validation, "TX", resp, "type")
	return err
}

func (icc *InferenceCosmosClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	return icc.manager.BankBalances(ctx, address)
}

func (icc *InferenceCosmosClient) SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitSeed(transaction *inference.MsgSubmitSeed) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) CreateTrainingTask(transaction *inference.MsgCreateTrainingTask) (*inference.MsgCreateTrainingTaskResponse, error) {
	transaction.Creator = icc.Address
	msg := &inference.MsgCreateTrainingTaskResponse{}

	if err := icc.SendTransactionSyncNoRetry(transaction, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (icc *InferenceCosmosClient) ClaimTrainingTaskForAssignment(transaction *inference.MsgClaimTrainingTaskForAssignment) (*inference.MsgClaimTrainingTaskForAssignmentResponse, error) {
	transaction.Creator = icc.Address
	msg := &inference.MsgClaimTrainingTaskForAssignmentResponse{}
	if err := icc.SendTransactionSyncNoRetry(transaction, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (icc *InferenceCosmosClient) AssignTrainingTask(transaction *inference.MsgAssignTrainingTask) (*inference.MsgAssignTrainingTaskResponse, error) {
	transaction.Creator = icc.Address
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
	transaction.Validator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

// GetBridgeAddresses retrieves all bridge addresses for a specific chain
func (icc *InferenceCosmosClient) GetBridgeAddresses(ctx context.Context, chainId string) ([]types.BridgeContractAddress, error) {
	queryClient := icc.NewInferenceQueryClient()

	resp, err := queryClient.BridgeAddressesByChain(ctx, &types.QueryBridgeAddressesByChainRequest{
		ChainId: chainId,
	})
	if err != nil {
		return nil, err
	}

	return resp.Addresses, nil
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
	// Recommended: ensure icc.ctx is already pinned to a single height via metadata
	// (caller can wrap icc.ctx with metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))).

	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]types.PartialUpgrade, *query.PageResponse, error) {
		resp, err := icc.NewInferenceQueryClient().PartialUpgradeAll(icc.ctx, &types.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})
	if err != nil {
		return nil, err
	}

	return &types.QueryAllPartialUpgradeResponse{
		PartialUpgrade: allUpgrades,
		Pagination:     &query.PageResponse{Total: uint64(len(allUpgrades))},
	}, nil
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

func (icc *InferenceCosmosClient) SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitVerificationVector(transaction *blstypes.MsgSubmitVerificationVector) (*sdk.TxResponse, error) {
	transaction.Creator = icc.Address
	resp, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	if err != nil {
		return nil, err
	}
	return resp, err
}

func (icc *InferenceCosmosClient) SubmitGroupKeyValidationSignature(transaction *blstypes.MsgSubmitGroupKeyValidationSignature) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitPartialSignature(requestId []byte, slotIndices []uint32, partialSignature []byte) error {
	transaction := &blstypes.MsgSubmitPartialSignature{
		Creator:          icc.Address,
		RequestId:        requestId,
		SlotIndices:      slotIndices,
		PartialSignature: partialSignature,
	}
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) NewBLSQueryClient() blstypes.QueryClient {
	return blstypes.NewQueryClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) NewRestrictionsQueryClient() restrictionstypes.QueryClient {
	return restrictionstypes.NewQueryClient(icc.manager.GetClientContext())
}

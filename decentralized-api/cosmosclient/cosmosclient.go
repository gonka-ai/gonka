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
	inferenceapi "github.com/productscience/inference/api/inference/inference"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

type InferenceCosmosClient struct {
	ctx             context.Context
	apiAccount      *apiconfig.ApiAccount
	Address         string
	manager         tx_manager.TxManager
	batchConsumer   *tx_manager.BatchConsumer
	batchingEnabled bool
}

func NewInferenceCosmosClientWithRetry(
	ctx context.Context,
	addressPrefix string,
	maxRetries int,
	delay time.Duration,
	config *apiconfig.ConfigManager) (*InferenceCosmosClient, error) {
	var client *InferenceCosmosClient
	var err error
	logging.Info("Connecting to cosmos sdk node", inferencetypes.System, "config", config, "height", config.GetHeight())
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

// queryChainMinGasPrice queries the inference module params for
// FeeParams.MinGasPriceNgonka. Distinguishes three cases via its return:
//   - (N, nil)         : chain returned a non-nil FeeParams, use N
//   - (0, nil)         : chain returned nil FeeParams (pre-upgrade chain), use 0
//   - (0, non-nil err) : query failed — caller must decide how to handle
//
// A query failure is NOT silently treated as "zero fees" because that can
// produce txs that get rejected on chain. Callers should either abort
// startup or fall back to a previously-known value explicitly.
func queryChainMinGasPrice(ctx context.Context, cc *cosmosclient.Client) (int64, error) {
	queryClient := inferencetypes.NewQueryClient(cc.Context())
	resp, err := queryClient.Params(ctx, &inferencetypes.QueryParamsRequest{})
	if err != nil {
		return 0, fmt.Errorf("query chain FeeParams: %w", err)
	}
	if resp == nil || resp.Params.FeeParams == nil {
		return 0, nil
	}
	return int64(resp.Params.FeeParams.MinGasPriceNgonka), nil
}

func NewInferenceCosmosClient(ctx context.Context, addressPrefix string, config *apiconfig.ConfigManager) (*InferenceCosmosClient, error) {
	nodeConfig := config.GetChainNodeConfig()
	keyringDir, err := expandPath(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	configGasPrice := nodeConfig.GetMinGasPriceNgonka()
	if configGasPrice != 0 {
		log.Printf("Ignoring configured DAPI_CHAIN_NODE__MIN_GAS_PRICE_NGONKA=%d; fees are disabled for this rollout, using 0", configGasPrice)
	}
	// Note: temporary due to issue in gas estimations.
	effectiveGasPrice := int64(0)

	log.Printf("Initializing cosmos Client."+
		"NodeUrl = %s. KeyringBackend = %s. KeyringDir = %s", nodeConfig.Url, nodeConfig.KeyringBackend, keyringDir)
	cosmoclient, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix(addressPrefix),
		cosmosclient.WithKeyringServiceName("inferenced"),
		cosmosclient.WithNodeAddress(nodeConfig.Url),
		cosmosclient.WithKeyringDir(keyringDir),
		cosmosclient.WithGasPrices(fmt.Sprintf("%dngonka", effectiveGasPrice)),
		cosmosclient.WithGas("auto"),
		cosmosclient.WithGasAdjustment(5),
	)
	if err != nil {
		log.Printf("Error creating cosmos client: %s", err)
		return nil, err
	}

	// Use the chain value only if governance enables a non-zero gas price.
	chainGasPrice, queryErr := queryChainMinGasPrice(ctx, &cosmoclient)
	if queryErr != nil {
		return nil, fmt.Errorf("failed to query chain for FeeParams.MinGasPriceNgonka at startup "+
			"(required for automatic DAPI gas price configuration): %w", queryErr)
	}
	if chainGasPrice > 0 {
		effectiveGasPrice = chainGasPrice
		log.Printf("Using on-chain FeeParams.MinGasPriceNgonka = %d", effectiveGasPrice)
		// Re-create the cosmoclient with the correct gas price for the
		// TxFactory to produce valid transactions.
		cosmoclient, err = cosmosclient.New(
			ctx,
			cosmosclient.WithAddressPrefix(addressPrefix),
			cosmosclient.WithKeyringServiceName("inferenced"),
			cosmosclient.WithNodeAddress(nodeConfig.Url),
			cosmosclient.WithKeyringDir(keyringDir),
			cosmosclient.WithGasPrices(fmt.Sprintf("%dngonka", effectiveGasPrice)),
			cosmosclient.WithGas("auto"),
			cosmosclient.WithGasAdjustment(5),
		)
		if err != nil {
			return nil, fmt.Errorf("error recreating cosmos client with chain gas price: %w", err)
		}
	} else {
		log.Printf("Chain FeeParams.MinGasPriceNgonka is 0 or unset; DAPI will send zero-fee transactions.")
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

	// Ensure natsConn is closed on any error to unbind consumers
	var success bool
	defer func() {
		if !success {
			natsConn.Close()
		}
	}()

	mn, err := tx_manager.StartTxManager(ctx, &cosmoclient, apiAccount, time.Second*60, natsConn, accAddress, effectiveGasPrice, config.GetHeight)
	if err != nil {
		return nil, err
	}

	client := &InferenceCosmosClient{
		ctx:        ctx,
		Address:    accAddress,
		apiAccount: apiAccount,
		manager:    mn,
	}

	batchingCfg := config.GetTxBatchingConfig()
	if !batchingCfg.Disabled {
		batchConfig := tx_manager.BatchConfig{
			FlushSize:                batchingCfg.FlushSize,
			FlushTimeout:             time.Duration(batchingCfg.FlushTimeoutSeconds) * time.Second,
			ValidationV2FlushSize:    batchingCfg.ValidationV2FlushSize,
			ValidationV2FlushTimeout: time.Duration(batchingCfg.ValidationV2FlushTimeoutSeconds) * time.Second,
		}
		batchConsumer := tx_manager.NewBatchConsumer(
			mn.GetJetStream(),
			cosmoclient.Context().Codec,
			mn,
			batchConfig,
		)
		if err := batchConsumer.Start(); err != nil {
			return nil, fmt.Errorf("failed to start batch consumer: %w", err)
		}
		client.batchConsumer = batchConsumer
		client.batchingEnabled = true
		logging.Info("Transaction batching enabled", inferencetypes.Messages,
			"flushSize", batchingCfg.FlushSize,
			"flushTimeoutSeconds", batchingCfg.FlushTimeoutSeconds,
			"validationV2FlushTimeoutSeconds", batchingCfg.ValidationV2FlushTimeoutSeconds)
	}

	success = true
	return client, nil
}

type CosmosMessageClient interface {
	SignBytes(seed []byte) ([]byte, error)
	DecryptBytes(ciphertext []byte) ([]byte, error)
	EncryptBytes(plaintext []byte) ([]byte, error)
	StartInference(transaction *inferenceapi.MsgStartInference) error
	FinishInference(transaction *inferenceapi.MsgFinishInference) error
	ReportValidation(transaction *inferenceapi.MsgValidation) error
	SubmitNewUnfundedParticipant(transaction *inferenceapi.MsgSubmitNewUnfundedParticipant) error
	SubmitPocValidationsV2(transaction *inferencetypes.MsgSubmitPocValidationsV2) error
	SubmitPoCV2StoreCommit(transaction *inferencetypes.MsgPoCV2StoreCommit) error
	SubmitMLNodeWeightDistribution(transaction *inferencetypes.MsgMLNodeWeightDistribution) error
	SubmitSeed(transaction *inferenceapi.MsgSubmitSeed) error
	ClaimRewards(transaction *inferenceapi.MsgClaimRewards) error
	SubmitUnitOfComputePriceProposal(transaction *inferenceapi.MsgSubmitUnitOfComputePriceProposal) error
	BridgeExchange(transaction *inferencetypes.MsgBridgeExchange) error
	GetBridgeAddresses(ctx context.Context, chainId string) ([]inferencetypes.BridgeContractAddress, error)
	NewInferenceQueryClient() inferencetypes.QueryClient
	NewCometQueryClient() cmtservice.ServiceClient
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
	SendTransactionAsyncWithRetry(rawTx sdk.Msg, deadlineBlock ...int64) (*sdk.TxResponse, error)
	SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionSyncNoRetry(transaction proto.Message, dstMsg proto.Message) error
	Status(ctx context.Context) (*ctypes.ResultStatus, error)
	GetContext() context.Context
	GetKeyring() *keyring.Keyring
	GetClientContext() sdkclient.Context
	GetAccountAddress() string
	GetAccountPubKey() cryptotypes.PubKey
	GetSignerPubKey() cryptotypes.PubKey
	GetSignerAddress() string
	SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error
	RespondDealerComplaints(transaction *blstypes.MsgRespondDealerComplaints) error
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
		logging.Error("Failed to get account address", inferencetypes.Messages, "error", err)
		return ""
	}
	return address
}

func (icc *InferenceCosmosClient) GetAccountPubKey() cryptotypes.PubKey {
	return icc.apiAccount.AccountKey
}

func (icc *InferenceCosmosClient) GetSignerPubKey() cryptotypes.PubKey {
	if icc.apiAccount == nil || icc.apiAccount.SignerAccount == nil || icc.apiAccount.SignerAccount.Record == nil {
		logging.Error("Signer account is not configured", inferencetypes.Messages)
		return nil
	}

	pubKey, err := icc.apiAccount.SignerAccount.Record.GetPubKey()
	if err != nil {
		logging.Error("Failed to get signer public key", inferencetypes.Messages, "error", err)
		return nil
	}
	return pubKey
}

func (icc *InferenceCosmosClient) GetSignerAddress() string {
	address, err := icc.apiAccount.SignerAddressBech32()
	if err != nil {
		logging.Error("Failed to get signer address", inferencetypes.Messages, "error", err)
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

func (icc *InferenceCosmosClient) StartInference(transaction *inferenceapi.MsgStartInference) error {
	transaction.Creator = icc.Address
	if icc.batchingEnabled {
		return icc.batchConsumer.PublishStartInference(transaction)
	}
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inferenceapi.MsgFinishInference) error {
	transaction.Creator = icc.Address
	transaction.ExecutedBy = icc.Address
	if icc.batchingEnabled {
		return icc.batchConsumer.PublishFinishInference(transaction)
	}
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inferenceapi.MsgValidation) error {
	transaction.Creator = icc.Address
	logging.Info("Reporting validation", inferencetypes.Validation, "value", transaction.Value, "type", fmt.Sprintf("%T", transaction), "creator", transaction.Creator)
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitNewUnfundedParticipant(transaction *inferenceapi.MsgSubmitNewUnfundedParticipant) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) ClaimRewards(transaction *inferenceapi.MsgClaimRewards) error {
	transaction.Creator = icc.Address
	resp, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	logging.Info("Claimed rewards", inferencetypes.Validation, "TX", resp, "type")
	return err
}

func (icc *InferenceCosmosClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	return icc.manager.BankBalances(ctx, address)
}

func (icc *InferenceCosmosClient) SubmitPocValidationsV2(transaction *inferencetypes.MsgSubmitPocValidationsV2) error {
	transaction.Creator = icc.Address
	if icc.batchingEnabled {
		return icc.batchConsumer.PublishPocValidationV2(transaction)
	}
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitPoCV2StoreCommit(transaction *inferencetypes.MsgPoCV2StoreCommit) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitMLNodeWeightDistribution(transaction *inferencetypes.MsgMLNodeWeightDistribution) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitSeed(transaction *inferenceapi.MsgSubmitSeed) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitUnitOfComputePriceProposal(transaction *inferenceapi.MsgSubmitUnitOfComputePriceProposal) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) BridgeExchange(transaction *inferencetypes.MsgBridgeExchange) error {
	transaction.Validator = icc.Address
	_, err := icc.manager.SendTransactionAsyncNoRetry(transaction)
	return err
}

// GetBridgeAddresses retrieves all bridge addresses for a specific chain
func (icc *InferenceCosmosClient) GetBridgeAddresses(ctx context.Context, chainId string) ([]inferencetypes.BridgeContractAddress, error) {
	queryClient := icc.NewInferenceQueryClient()

	resp, err := queryClient.BridgeAddressesByChain(ctx, &inferencetypes.QueryBridgeAddressesByChainRequest{
		ChainId: chainId,
	})
	if err != nil {
		return nil, err
	}

	return resp.Addresses, nil
}

func (icc *InferenceCosmosClient) SendTransactionAsyncWithRetry(msg sdk.Msg, deadlineBlock ...int64) (*sdk.TxResponse, error) {
	return icc.manager.SendTransactionAsyncWithRetry(msg, deadlineBlock...)
}

func (icc *InferenceCosmosClient) SendTransactionAsyncNoRetry(msg sdk.Msg) (*sdk.TxResponse, error) {
	return icc.manager.SendTransactionAsyncNoRetry(msg)
}

func (icc *InferenceCosmosClient) GetUpgradePlan() (*upgradetypes.QueryCurrentPlanResponse, error) {
	return icc.NewUpgradeQueryClient().CurrentPlan(icc.ctx, &upgradetypes.QueryCurrentPlanRequest{})
}

func (icc *InferenceCosmosClient) GetPartialUpgrades() (*inferencetypes.QueryAllPartialUpgradeResponse, error) {
	// Recommended: ensure icc.ctx is already pinned to a single height via metadata
	// (caller can wrap icc.ctx with metadata.Pairs(grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(height, 10))).

	allUpgrades, err := utils.GetAllWithPagination(func(pageReq *query.PageRequest) ([]inferencetypes.PartialUpgrade, *query.PageResponse, error) {
		resp, err := icc.NewInferenceQueryClient().PartialUpgradeAll(icc.ctx, &inferencetypes.QueryAllPartialUpgradeRequest{Pagination: pageReq})
		if err != nil {
			return nil, nil, err
		}
		return resp.PartialUpgrade, resp.Pagination, nil
	})
	if err != nil {
		return nil, err
	}

	return &inferencetypes.QueryAllPartialUpgradeResponse{
		PartialUpgrade: allUpgrades,
		Pagination:     &query.PageResponse{Total: uint64(len(allUpgrades))},
	}, nil
}

func (icc *InferenceCosmosClient) NewUpgradeQueryClient() upgradetypes.QueryClient {
	return upgradetypes.NewQueryClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) NewInferenceQueryClient() inferencetypes.QueryClient {
	return inferencetypes.NewQueryClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) NewCometQueryClient() cmtservice.ServiceClient {
	return cmtservice.NewServiceClient(icc.manager.GetClientContext())
}

func (icc *InferenceCosmosClient) SendTransactionSyncNoRetry(transaction proto.Message, dstMsg proto.Message) error {
	result, err := icc.manager.SendTransactionSyncNoRetry(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", inferencetypes.Messages, "error", err, "result", result)
		return err
	}

	err = tx_manager.ParseMsgResponse(result.TxResult.Data, 0, dstMsg)
	if err != nil {
		logging.Error("Failed to parse message response", inferencetypes.Messages, "error", err)
		return err
	}
	return nil
}

func (icc *InferenceCosmosClient) SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error {
	transaction.Creator = icc.Address
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

func (icc *InferenceCosmosClient) RespondDealerComplaints(transaction *blstypes.MsgRespondDealerComplaints) error {
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

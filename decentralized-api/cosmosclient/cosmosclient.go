package cosmosclient

import (
	"context"
	"crypto/rand"
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

type InferenceCosmosClient struct {
	Client     *cosmosclient.Client
	ApiAccount *apiconfig.ApiAccount
	Address    string
	Context    context.Context
	TxFactory  *tx.Factory
	config     *apiconfig.ConfigManager
}

func NewInferenceCosmosClientWithRetry(ctx context.Context, addressPrefix string, maxRetries int, delay time.Duration, config *apiconfig.ConfigManager) (*InferenceCosmosClient, error) {
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
	client, err := cosmosclient.New(
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
	err = updateKeyringIfNeeded(&client, keyringDir, config)
	if err != nil {
		log.Printf("Error updating keyring: %s", err)
		return nil, err
	}

	apiAccount, err := apiconfig.NewApiAccount(addressPrefix, nodeConfig, &client)
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

	return &InferenceCosmosClient{
		Client:     &client,
		ApiAccount: apiAccount,
		Address:    accAddress,
		Context:    ctx,
		config:     config,
	}, nil
}

type CosmosMessageClient interface {
	SignBytes(seed []byte) ([]byte, error)
	DecryptBytes(ciphertext []byte) ([]byte, error)
	EncryptBytes(plaintext []byte) ([]byte, error)
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
	SendTransaction(msg sdk.Msg) (*sdk.TxResponse, error)
	GetContext() *context.Context
	GetAccountAddress() string
	GetAccountPubKey() cryptotypes.PubKey
	GetSignerAddress() string
	GetCosmosClient() *cosmosclient.Client
	GetKeyring() *keyring.Keyring
	SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error
	SubmitVerificationVector(transaction *blstypes.MsgSubmitVerificationVector) (*blstypes.MsgSubmitVerificationVectorResponse, error)
	SubmitGroupKeyValidationSignature(transaction *blstypes.MsgSubmitGroupKeyValidationSignature) error
	SubmitPartialSignature(requestId []byte, slotIndices []uint32, partialSignature []byte) error
	NewBLSQueryClient() blstypes.QueryClient
}

func (icc *InferenceCosmosClient) GetContext() *context.Context {
	return &icc.Context
}

func (icc *InferenceCosmosClient) GetKeyring() *keyring.Keyring {
	return &icc.Client.AccountRegistry.Keyring
}

func (icc *InferenceCosmosClient) GetAccountAddress() string {
	address, err := icc.ApiAccount.AccountAddressBech32()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "error", err)
		return ""
	}
	return address
}

func (icc *InferenceCosmosClient) GetAccountPubKey() cryptotypes.PubKey {
	return icc.ApiAccount.AccountKey
}

func (icc *InferenceCosmosClient) GetSignerAddress() string {
	address, err := icc.ApiAccount.SignerAddressBech32()
	if err != nil {
		logging.Error("Failed to get signer address", types.Messages, "error", err)
		return ""
	}
	return address
}

func (icc *InferenceCosmosClient) GetCosmosClient() *cosmosclient.Client {
	return icc.Client
}

func (icc *InferenceCosmosClient) SignBytes(seed []byte) ([]byte, error) {
	accName := icc.ApiAccount.SignerAccount.Name
	kr := *icc.GetKeyring()
	bytes, _, err := kr.Sign(accName, seed, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) DecryptBytes(ciphertext []byte) ([]byte, error) {
	name := icc.ApiAccount.SignerAccount.Name
	// Use the new keyring Decrypt method
	bytes, err := icc.Client.Context().Keyring.Decrypt(name, ciphertext, nil, nil)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) EncryptBytes(plaintext []byte) ([]byte, error) {
	name := icc.ApiAccount.SignerAccount.Name
	// Use the new keyring Encrypt method with rand.Reader
	bytes, err := icc.Client.Context().Keyring.Encrypt(rand.Reader, name, plaintext, nil, nil)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) StartInference(transaction *inference.MsgStartInference) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.Address
	transaction.ExecutedBy = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inference.MsgValidation) error {
	transaction.Creator = icc.Address
	logging.Info("Reporting validation", types.Validation, "value", transaction.Value, "type", fmt.Sprintf("%T", transaction), "creator", transaction.Creator)
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) ClaimRewards(transaction *inference.MsgClaimRewards) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	return icc.Client.BankBalances(ctx, address, nil)
}

func (icc *InferenceCosmosClient) SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitSeed(transaction *inference.MsgSubmitSeed) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) CreateTrainingTask(transaction *inference.MsgCreateTrainingTask) (*inference.MsgCreateTrainingTaskResponse, error) {
	transaction.Creator = icc.Address
	result, err := icc.SendTransaction(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err, "result", result)
		return nil, err
	}

	transactionAppliedResult, err := icc.Client.WaitForTx(icc.Context, result.TxHash)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "error", err, "result", transactionAppliedResult)
		return nil, err
	}

	msg := inference.MsgCreateTrainingTaskResponse{}
	err = ParseMsgResponse[*inference.MsgCreateTrainingTaskResponse](transactionAppliedResult.TxResult.Data, 0, &msg)
	if err != nil {
		logging.Error("Failed to parse message response", types.Messages, "error", err)
		return nil, err
	}

	return &msg, err
}

func (icc *InferenceCosmosClient) ClaimTrainingTaskForAssignment(transaction *inference.MsgClaimTrainingTaskForAssignment) (*inference.MsgClaimTrainingTaskForAssignmentResponse, error) {
	transaction.Creator = icc.Address
	result, err := icc.SendTransaction(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err, "result", result)
		return nil, err
	}

	response := inference.MsgClaimTrainingTaskForAssignmentResponse{}
	err = WaitForResponse(icc.Context, icc.Client, result.TxHash, &response)
	return &response, err
}

func (icc *InferenceCosmosClient) AssignTrainingTask(transaction *inference.MsgAssignTrainingTask) (*inference.MsgAssignTrainingTaskResponse, error) {
	transaction.Creator = icc.Address
	result, err := icc.SendTransaction(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err, "result", result)
		return nil, err
	}

	response := inference.MsgAssignTrainingTaskResponse{}
	err = WaitForResponse(icc.Context, icc.Client, result.TxHash, &response)
	return &response, err
}

func (icc *InferenceCosmosClient) BridgeExchange(transaction *types.MsgBridgeExchange) error {
	transaction.Validator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

var accountRetriever = authtypes.AccountRetriever{}

func (icc *InferenceCosmosClient) BroadcastMessage(ctx context.Context, msg sdk.Msg) (*sdk.TxResponse, error) {
	factory, err := icc.getFactory()
	if err != nil {
		return nil, err
	}

	var finalMsg sdk.Msg = msg
	if !icc.ApiAccount.IsSignerTheMainAccount() {
		granteeAddress, err := icc.ApiAccount.SignerAddress()
		if err != nil {
			return nil, fmt.Errorf("failed to get signer address: %w", err)
		}

		execMsg := authztypes.NewMsgExec(granteeAddress, []sdk.Msg{msg})
		finalMsg = &execMsg
		logging.Info("Using authz MsgExec", types.Messages, "grantee", granteeAddress.String(), "originalMsgType", sdk.MsgTypeURL(msg))
	}

	unsignedTx, err := factory.BuildUnsignedTx(finalMsg)
	if err != nil {
		return nil, err
	}
	txBytes, err := icc.getSignedBytes(ctx, unsignedTx, factory)
	if err != nil {
		return nil, err
	}
	return icc.Client.Context().BroadcastTxSync(txBytes)
}

// TODO: This is likely not as guaranteed to be unique as we want. Will fix
func getTimestamp() time.Time {
	// Use the current time in seconds since epoch
	return time.Now().Add(time.Second * 60) // Adding 60 seconds to ensure the transaction is valid for a while
}

func (icc *InferenceCosmosClient) getSignedBytes(ctx context.Context, unsignedTx client.TxBuilder, factory *tx.Factory) ([]byte, error) {
	// Gas is not charged, but without a high gas limit the transactions fail
	unsignedTx.SetGasLimit(1000000000)
	unsignedTx.SetFeeAmount(sdk.Coins{})
	timestamp := getTimestamp()
	unsignedTx.SetUnordered(true)
	unsignedTx.SetTimeoutTimestamp(timestamp)
	name := icc.ApiAccount.SignerAccount.Name
	logging.Debug("Signing transaction", types.Messages, "name", name)
	err := tx.Sign(ctx, *factory, name, unsignedTx, false)
	if err != nil {
		logging.Error("Failed to sign transaction", types.Messages, "error", err)
		return nil, err
	}
	txBytes, err := icc.Client.Context().TxConfig.TxEncoder()(unsignedTx.GetTx())
	if err != nil {
		logging.Error("Failed to encode transaction", types.Messages, "error", err)
		return nil, err
	}
	return txBytes, nil
}

func (c *InferenceCosmosClient) getFactory() (*tx.Factory, error) {
	// Now that we don't need the sequence, we only need to create the factory if it doesn't exist
	if c.TxFactory != nil {
		return c.TxFactory, nil
	}
	address, err := c.ApiAccount.SignerAddress()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "error", err)
		return nil, err
	}
	accountNumber, _, err := accountRetriever.GetAccountNumberSequence(c.Client.Context(), address)
	if err != nil {
		logging.Error("Failed to get account number and sequence", types.Messages, "error", err)
		return nil, err
	}
	factory := c.Client.TxFactory.
		WithAccountNumber(accountNumber).
		WithGasAdjustment(10).
		WithFees("").
		WithGasPrices("").
		WithGas(0).
		WithUnordered(true).
		WithKeybase(*c.GetKeyring())
	c.TxFactory = &factory
	return &factory, nil
}

func (icc *InferenceCosmosClient) SendTransaction(msg sdk.Msg) (*sdk.TxResponse, error) {
	// create a guid
	id := uuid.New().String()

	logging.Debug("Start Broadcast", types.Messages, "id", id)
	response, err := icc.BroadcastMessage(icc.Context, msg)
	logging.Debug("Finish broadcast", types.Messages, "id", id)
	if err != nil {
		logging.Error("Failed to broadcast transaction", types.Messages, "error", err)
		return response, err
	}

	if response == nil {
		logging.Warn("Broadcast returned nil response, potentially async mode or error", types.Messages, "id", id)
		return nil, nil
	}

	logging.Debug("Transaction broadcast raw response", types.Messages, "id", id, "txHash", response.TxHash, "code", response.Code)

	if response.Code != 0 {
		logging.Error("Transaction failed during CheckTx or DeliverTx (sync/block mode)", types.Messages, "id", id, "response", response)
		return response, NewTransactionErrorFromResponse(response)
	}
	logging.Debug("Transaction broadcast successful (or pending if async)", types.Messages, "id", id, "txHash", response.TxHash)
	return response, nil
}

func (icc *InferenceCosmosClient) GetUpgradePlan() (*upgradetypes.QueryCurrentPlanResponse, error) {
	return icc.NewUpgradeQueryClient().CurrentPlan(icc.Context, &upgradetypes.QueryCurrentPlanRequest{})
}

func (icc *InferenceCosmosClient) GetPartialUpgrades() (*types.QueryAllPartialUpgradeResponse, error) {
	return icc.NewInferenceQueryClient().PartialUpgradeAll(icc.Context, &types.QueryAllPartialUpgradeRequest{})
}

func (icc *InferenceCosmosClient) NewUpgradeQueryClient() upgradetypes.QueryClient {
	return upgradetypes.NewQueryClient(icc.Client.Context())
}

func (icc *InferenceCosmosClient) NewInferenceQueryClient() types.QueryClient {
	return types.NewQueryClient(icc.Client.Context())
}

func (icc *InferenceCosmosClient) NewCometQueryClient() cmtservice.ServiceClient {
	return cmtservice.NewServiceClient(icc.Client.Context())
}

func (icc *InferenceCosmosClient) QueryRandomExecutor() (*types.Participant, error) {
	queryClient := icc.NewInferenceQueryClient()
	resp, err := queryClient.GetRandomExecutor(icc.Context, &types.QueryGetRandomExecutorRequest{})
	if err != nil {
		return nil, err
	}
	return &resp.Executor, nil
}

func ParseMsgFromTxResponse[T proto.Message](txResp *sdk.TxResponse, msgIndex int, dstMsg T) error {
	rawData, err := base64.StdEncoding.DecodeString(txResp.Data)
	if err != nil {
		return fmt.Errorf("failed to base64-decode TxResponse.Data: %w", err)
	}

	return ParseMsgResponse(rawData, msgIndex, dstMsg)
}

func ParseMsgResponse[T proto.Message](data []byte, msgIndex int, dstMsg T) error {
	var txMsgData sdk.TxMsgData
	if err := proto.Unmarshal(data, &txMsgData); err != nil {
		logging.Error("Failed to unmarshal TxMsgData", types.Messages, "error", err, "data", data)
		return fmt.Errorf("failed to unmarshal TxMsgData: %w", err)
	}

	logging.Info("Found messages", types.Messages, "len(messages)", len(txMsgData.MsgResponses), "messages", txMsgData.MsgResponses)
	if msgIndex < 0 || msgIndex >= len(txMsgData.MsgResponses) {
		logging.Error("Message index out of range", types.Messages, "msgIndex", msgIndex, "len(messages)", len(txMsgData.MsgResponses))
		return fmt.Errorf(
			"message index %d out of range: got %d responses",
			msgIndex, len(txMsgData.MsgResponses),
		)
	}

	anyResp := txMsgData.MsgResponses[msgIndex]

	if err := proto.Unmarshal(anyResp.Value, dstMsg); err != nil {
		logging.Error("Failed to unmarshal response", types.Messages, "error", err, "msgIndex", msgIndex, "response", anyResp.Value)
		return fmt.Errorf("failed to unmarshal response at index %d: %w", msgIndex, err)
	}

	return nil
}

func WaitForResponse[T proto.Message](ctx context.Context, client *cosmosclient.Client, txHash string, dstMsg T) error {
	transactionAppliedResult, err := client.WaitForTx(ctx, txHash)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "error", err, "result", transactionAppliedResult)
		return err
	}

	txResult := transactionAppliedResult.TxResult
	if txResult.Code != 0 {
		logging.Error("Transaction failed on-chain", types.Messages, "txHash", txHash, "code", txResult.Code, "codespace", txResult.Codespace, "rawLog", txResult.Log)
		return NewTransactionErrorFromResult(transactionAppliedResult)
	}

	return ParseMsgResponse[T](transactionAppliedResult.TxResult.Data, 0, dstMsg)
}

func SendTransactionBlocking[In proto.Message, Out proto.Message](ctx context.Context, msgClient CosmosMessageClient, msg In, dstMsg Out) error {
	txResponse, err := msgClient.SendTransaction(msg)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err)
		return err
	}

	err = WaitForResponse(ctx, msgClient.GetCosmosClient(), txResponse.TxHash, dstMsg)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "error", err)
		return err
	}
	return nil
}

func (icc *InferenceCosmosClient) SubmitDealerPart(transaction *blstypes.MsgSubmitDealerPart) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitVerificationVector(transaction *blstypes.MsgSubmitVerificationVector) (*blstypes.MsgSubmitVerificationVectorResponse, error) {
	transaction.Creator = icc.Address
	result, err := icc.SendTransaction(transaction)
	if err != nil {
		logging.Error("Failed to send transaction", types.Messages, "error", err, "result", result)
		return nil, err
	}

	response := blstypes.MsgSubmitVerificationVectorResponse{}
	err = WaitForResponse(icc.Context, icc.Client, result.TxHash, &response)
	return &response, err
}

func (icc *InferenceCosmosClient) SubmitGroupKeyValidationSignature(transaction *blstypes.MsgSubmitGroupKeyValidationSignature) error {
	transaction.Creator = icc.Address
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) SubmitPartialSignature(requestId []byte, slotIndices []uint32, partialSignature []byte) error {
	transaction := &blstypes.MsgSubmitPartialSignature{
		Creator:          icc.Address,
		RequestId:        requestId,
		SlotIndices:      slotIndices,
		PartialSignature: partialSignature,
	}
	_, err := icc.SendTransaction(transaction)
	return err
}

func (icc *InferenceCosmosClient) NewBLSQueryClient() blstypes.QueryClient {
	return blstypes.NewQueryClient(icc.Client.Context())
}

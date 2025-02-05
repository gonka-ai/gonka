package cosmosclient

import (
	"context"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"decentralized-api/apiconfig"
	"errors"
	"fmt"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/productscience/inference/api/inference/inference"
	"log"
	"log/slog"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/productscience/inference/x/inference/types"
)

type InferenceCosmosClient struct {
	Client  *cosmosclient.Client
	Account *cosmosaccount.Account
	Address string
	Context context.Context
}

func NewInferenceCosmosClientWithRetry(ctx context.Context, addressPrefix string, maxRetries int, delay time.Duration, config *apiconfig.Config) (*InferenceCosmosClient, error) {
	var client *InferenceCosmosClient
	var err error
	slog.Info("Connecting to cosmos sdk node", "config", config, "height", config.CurrentHeight)
	for i := 0; i < maxRetries; i++ {
		client, err = NewInferenceCosmosClient(ctx, addressPrefix, config.ChainNode)
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

func NewInferenceCosmosClient(ctx context.Context, addressPrefix string, nodeConfig apiconfig.ChainNodeConfig) (*InferenceCosmosClient, error) {
	// Get absolute path to keyring directory
	keyringDir, err := expandPath(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	log.Printf("Initializing cosmos Client."+
		"NodeUrl = %s. KeyringBackend = %s. KeyringDir = %s", nodeConfig.Url, nodeConfig.KeyringBackend, keyringDir)
	client, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix(addressPrefix),
		cosmosclient.WithNodeAddress(nodeConfig.Url),
		cosmosclient.WithKeyringBackend(cosmosaccount.KeyringBackend(nodeConfig.KeyringBackend)),
		cosmosclient.WithKeyringDir(keyringDir),
		cosmosclient.WithGasPrices("0icoin"),
		cosmosclient.WithFees("0icoin"),
		cosmosclient.WithGas("auto"),
		cosmosclient.WithGasAdjustment(5),
	)
	if err != nil {
		return nil, err
	}

	account, err := client.AccountRegistry.GetByName(nodeConfig.AccountName)
	if err != nil {
		return nil, err
	}

	addr, err := account.Address(addressPrefix)
	if err != nil {
		return nil, err
	}

	return &InferenceCosmosClient{
		Client:  &client,
		Account: &account,
		Address: addr,
		Context: ctx,
	}, nil
}

type CosmosMessageClient interface {
	SignBytes(seed []byte) ([]byte, error)
	StartInference(transaction *inference.MsgStartInference) error
	FinishInference(transaction *inference.MsgFinishInference) error
	ReportValidation(transaction *inference.MsgValidation) error
	SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error
	SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error
	SubmitPoC(transaction *inference.MsgSubmitPoC) error
	SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error
	SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error
	SubmitSeed(transaction *inference.MsgSubmitSeed) error
	ClaimRewards(transaction *inference.MsgClaimRewards) error
	SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error
	NewInferenceQueryClient() types.QueryClient
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
	SendTransaction(msg sdk.Msg) error
	GetContext() *context.Context
	GetAddress() string
	GetAccount() *cosmosaccount.Account
}

func (icc *InferenceCosmosClient) GetContext() *context.Context {
	return &icc.Context
}

func (icc *InferenceCosmosClient) GetAddress() string {
	return icc.Address
}

func (icc *InferenceCosmosClient) GetAccount() *cosmosaccount.Account {
	return icc.Account
}

func (icc *InferenceCosmosClient) SignBytes(seed []byte) ([]byte, error) {
	name := icc.Account.Name
	// Kind of guessing here, not sure if this is the right way to sign bytes, will need to test
	bytes, _, err := icc.Client.Context().Keyring.Sign(name, seed, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (icc *InferenceCosmosClient) StartInference(transaction *inference.MsgStartInference) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.Address
	transaction.ExecutedBy = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inference.MsgValidation) error {
	transaction.Creator = icc.Address
	slog.Info("Validation: Reporting validation", "value", transaction.Value, "type", fmt.Sprintf("%T", transaction), "creator", transaction.Creator)
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitPoC(transaction *inference.MsgSubmitPoC) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) ClaimRewards(transaction *inference.MsgClaimRewards) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	return icc.Client.BankBalances(ctx, address, nil)
}

func (icc *InferenceCosmosClient) SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitSeed(transaction *inference.MsgSubmitSeed) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitUnitOfComputePriceProposal(transaction *inference.MsgSubmitUnitOfComputePriceProposal) error {
	transaction.Creator = icc.Address
	return icc.SendTransaction(transaction)
}

var sendTransactionMutex sync.Mutex = sync.Mutex{}
var accountRetriever = authtypes.AccountRetriever{}
var highestSequence int64 = -1

func (c *InferenceCosmosClient) BroadcastMessage(ctx context.Context, msg sdk.Msg) (*sdk.TxResponse, error) {
	factory, err := c.getFactory()
	if err != nil {
		return nil, err
	}
	unsignedTx, err := factory.BuildUnsignedTx(msg)
	if err != nil {
		return nil, err
	}
	txBytes, err := c.getSignedBytes(ctx, unsignedTx, factory)
	if err != nil {
		return nil, err
	}
	response, err := c.Client.Context().BroadcastTxSync(txBytes)
	if err == nil && response.Code == 0 {
		highestSequence = int64(factory.Sequence())
	}
	return response, err
}

func (c *InferenceCosmosClient) getSignedBytes(ctx context.Context, unsignedTx client.TxBuilder, factory *tx.Factory) ([]byte, error) {
	// Gas is not charged, but without a high gas limit the transactions fail
	unsignedTx.SetGasLimit(1000000000)
	unsignedTx.SetFeeAmount(sdk.Coins{})
	name := c.Account.Name
	slog.Debug("Signing transaction", "name", name)
	err := tx.Sign(ctx, *factory, name, unsignedTx, false)
	if err != nil {
		slog.Error("Failed to sign transaction", "error", err)
		return nil, err
	}
	txBytes, err := c.Client.Context().TxConfig.TxEncoder()(unsignedTx.GetTx())
	if err != nil {
		slog.Error("Failed to encode transaction", "error", err)
		return nil, err
	}
	return txBytes, nil
}

func (c *InferenceCosmosClient) getFactory() (*tx.Factory, error) {
	address, err := c.Account.Record.GetAddress()
	if err != nil {
		slog.Error("Failed to get account address", "error", err)
		return nil, err
	}
	accountNumber, sequence, err := accountRetriever.GetAccountNumberSequence(c.Client.Context(), address)
	if err != nil {
		slog.Error("Failed to get account number and sequence", "error", err)
		return nil, err
	}
	if int64(sequence) <= highestSequence {
		slog.Info("Sequence is lower than highest sequence", "sequence", sequence, "highestSequence", highestSequence)
		sequence = uint64(highestSequence + 1)
	}
	slog.Debug("Transaction sequence", "sequence", sequence, "accountNumber", accountNumber)
	factory := c.Client.TxFactory.
		WithSequence(sequence).
		WithAccountNumber(accountNumber).WithGasAdjustment(10).WithFees("").WithGasPrices("").WithGas(0)
	return &factory, nil
}

func (icc *InferenceCosmosClient) SendTransaction(msg sdk.Msg) error {
	// create a guid
	id := uuid.New().String()
	sendTransactionMutex.Lock()
	defer sendTransactionMutex.Unlock()

	slog.Debug("Start Broadcast", "id", id)
	response, err := icc.BroadcastMessage(icc.Context, msg)
	slog.Debug("Finish broadcast", "id", id)
	if err != nil {
		slog.Error("Failed to broadcast transaction", "error", err)
		return err
	}
	slog.Debug("Transaction broadcast successfully", "response", response.Data)
	if response.Code != 0 {
		slog.Error("Transaction failed", "response", response)
	}
	return nil
}

func (icc *InferenceCosmosClient) GetUpgradePlan() (*upgradetypes.QueryCurrentPlanResponse, error) {
	return icc.NewUpgradeQueryClient().CurrentPlan(icc.Context, &upgradetypes.QueryCurrentPlanRequest{})
}

func (icc *InferenceCosmosClient) NewUpgradeQueryClient() upgradetypes.QueryClient {
	return upgradetypes.NewQueryClient(icc.Client.Context())
}

func (icc *InferenceCosmosClient) NewInferenceQueryClient() types.QueryClient {
	return types.NewQueryClient(icc.Client.Context())
}

func (icc *InferenceCosmosClient) QueryRandomExecutor() (*types.Participant, error) {
	queryClient := icc.NewInferenceQueryClient()
	resp, err := queryClient.GetRandomExecutor(icc.Context, &types.QueryGetRandomExecutorRequest{})
	if err != nil {
		return nil, err
	}
	return &resp.Executor, nil
}

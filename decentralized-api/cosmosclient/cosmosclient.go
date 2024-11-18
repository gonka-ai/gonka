package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"errors"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
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

func NewInferenceCosmosClientWithRetry(
	ctx context.Context,
	addressPrefix string,
	maxRetries int,
	delay time.Duration,
	config apiconfig.Config,
) (*InferenceCosmosClient, error) {
	var client *InferenceCosmosClient
	var err error

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

func (icc *InferenceCosmosClient) StartInference(transaction *inference.MsgStartInference) error {
	transaction.Creator = icc.Address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.Address
	transaction.ExecutedBy = icc.Address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inference.MsgValidation) error {
	transaction.Creator = icc.Address
	slog.Info("Validation: Reporting validation", "value", transaction.Value, "type", fmt.Sprintf("%T", transaction), "creator", transaction.Creator)
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error {
	transaction.Creator = icc.Address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitNewUnfundedParticipant(transaction *inference.MsgSubmitNewUnfundedParticipant) error {
	transaction.Creator = icc.Address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitPoC(transaction *inference.MsgSubmitPoC) error {
	transaction.Creator = icc.Address
	return icc.sendTransaction(transaction)
}

var sendTransactionMutex sync.Mutex = sync.Mutex{}

func (icc *InferenceCosmosClient) sendTransaction(msg sdk.Msg) error {
	// create a guid
	id := uuid.New().String()
	sendTransactionMutex.Lock()
	slog.Debug("Start Broadcast", "id", id)
	response, err := icc.Client.BroadcastTx(icc.Context, *icc.Account, msg)
	slog.Debug("Finish broadcast", "id", id)
	sendTransactionMutex.Unlock()
	if err != nil {
		slog.Error("Failed to broadcast transaction", "error", err)
		return err
	}
	// TODO: maybe check response for success?
	_ = response
	slog.Debug("Transaction broadcast successfully", "response", response.Data)
	if response.Code != 0 {
		slog.Error("Transaction failed", "response", response)
	}
	return nil
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

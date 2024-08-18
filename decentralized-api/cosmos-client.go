package main

import (
	"context"
	"errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"inference/api/inference/inference"
	"log"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"inference/x/inference/types"
)

type InferenceCosmosClient struct {
	client  *cosmosclient.Client
	account *cosmosaccount.Account
	address string
	context context.Context
}

func NewInferenceCosmosClientWithRetry(
	ctx context.Context,
	addressPrefix string,
	maxRetries int,
	delay time.Duration,
	config Config,
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

func NewInferenceCosmosClient(ctx context.Context, addressPrefix string, nodeConfig ChainNodeConfig) (*InferenceCosmosClient, error) {
	// Get absolute path to keyring directory
	keyringDir, err := expandPath(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	log.Printf("Initializing cosmos client."+
		"NodeUrl = %s. KeyringBackend = %s. KeyringDir = %s", nodeConfig.Url, nodeConfig.KeyringBackend, keyringDir)
	client, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix(addressPrefix),
		cosmosclient.WithNodeAddress(nodeConfig.Url),
		cosmosclient.WithKeyringBackend(cosmosaccount.KeyringBackend(nodeConfig.KeyringBackend)),
		cosmosclient.WithKeyringDir(keyringDir),
		cosmosclient.WithGasPrices("0icoin"),
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
		client:  &client,
		account: &account,
		address: addr,
		context: ctx,
	}, nil
}

func (icc *InferenceCosmosClient) StartInference(transaction *inference.MsgStartInference) error {
	transaction.Creator = icc.address
	transaction.ReceivedBy = icc.address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.address
	transaction.ExecutedBy = icc.address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) ReportValidation(transaction *inference.MsgValidation) error {
	transaction.Creator = icc.address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) SubmitNewParticipant(transaction *inference.MsgSubmitNewParticipant) error {
	transaction.Creator = icc.address
	return icc.sendTransaction(transaction)
}

func (icc *InferenceCosmosClient) sendTransaction(msg sdk.Msg) error {
	response, err := icc.client.BroadcastTx(icc.context, *icc.account, msg)
	if err != nil {
		return err
	}
	// TODO: maybe check response for success?
	_ = response
	println(response.Data)
	return nil
}

func (icc *InferenceCosmosClient) NewInferenceQueryClient() types.QueryClient {
	return types.NewQueryClient(icc.client.Context())
}

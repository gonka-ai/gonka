package main

import (
	"context"
	"errors"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"inference/api/inference/inference"
	"log"
	"time"

	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
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

func NewInferenceCosmosClient(ctx context.Context, addressPrefix string, nodeConfig ChainNodeConfig) (*InferenceCosmosClient, error) {
	client, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix(addressPrefix),
		cosmosclient.WithNodeAddress(nodeConfig.Url),
		cosmosclient.WithKeyringBackend(cosmosaccount.KeyringBackend(nodeConfig.KeyringBackend)),
		cosmosclient.WithKeyringDir(nodeConfig.KeyringDir),
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
	response, err := icc.client.BroadcastTx(icc.context, *icc.account, transaction)
	if err != nil {
		return err
	}
	// TODO: maybe check response for success?
	_ = response
	println(response.Data)
	return nil
}

func (icc *InferenceCosmosClient) FinishInference(transaction *inference.MsgFinishInference) error {
	transaction.Creator = icc.address
	transaction.ExecutedBy = icc.address
	response, err := icc.client.BroadcastTx(icc.context, *icc.account, transaction)
	if err != nil {
		return err
	}
	// TODO: maybe check response for success?
	_ = response
	println(response.Data)
	return nil
}

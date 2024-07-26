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
	accountName string,
	maxRetries int,
	delay time.Duration,
) (*InferenceCosmosClient, error) {
	var client *InferenceCosmosClient
	var err error

	for i := 0; i < maxRetries; i++ {
		client, err = NewInferenceCosmosClient(ctx, addressPrefix, accountName)
		if err == nil {
			return client, nil
		}
		log.Printf("Failed to connect to cosmos sdk node, retrying in %s. err = %s", delay, err)
		time.Sleep(delay)
	}

	return nil, errors.New("failed to connect to cosmos sdk node after multiple retries")
}

func NewInferenceCosmosClient(ctx context.Context, addressPrefix string, accountName string) (*InferenceCosmosClient, error) {
	client, err := cosmosclient.New(ctx, cosmosclient.WithAddressPrefix(addressPrefix))
	if err != nil {
		return nil, err
	}

	account, err := client.Account(accountName)
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

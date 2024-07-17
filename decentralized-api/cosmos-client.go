package main

import (
	"context"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"inference/api/inference/inference"

	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
)

type InferenceCosmosClient struct {
	client  *cosmosclient.Client
	account *cosmosaccount.Account
	address string
	context context.Context
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

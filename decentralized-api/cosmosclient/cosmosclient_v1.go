package cosmosclient

import (
	"github.com/productscience/inference/api/inference/inference"
)

// V1 PoC methods for chain message submission.
// These methods support PoC V1 (on-chain batches) when poc_v2_enabled=false.

// SubmitPocBatch submits a V1 PoCBatch to the chain.
// Uses batch consumer when batching is enabled for efficiency.
func (icc *InferenceCosmosClient) SubmitPocBatch(transaction *inference.MsgSubmitPocBatch) error {
	transaction.Creator = icc.Address
	if icc.batchingEnabled {
		return icc.batchConsumer.PublishPocBatch(transaction)
	}
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

// SubmitPoCValidation submits a V1 PoCValidation to the chain.
// Uses batch consumer when batching is enabled for efficiency.
func (icc *InferenceCosmosClient) SubmitPoCValidation(transaction *inference.MsgSubmitPocValidation) error {
	transaction.Creator = icc.Address
	if icc.batchingEnabled {
		return icc.batchConsumer.PublishPocValidation(transaction)
	}
	_, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
	return err
}

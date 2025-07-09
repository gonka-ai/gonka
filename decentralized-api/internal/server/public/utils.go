package public

import (
	"github.com/productscience/inference/x/inference/calculations"
)

func validateTransferRequest(request *ChatRequest, devPubkey string) error {
	components := calculations.SignatureComponents{
		Payload:         string(request.Body),
		Timestamp:       request.Timestamp,
		TransferAddress: request.TransferAddress,
		ExecutorAddress: "",
	}
	return calculations.ValidateSignature(components, calculations.TransferAgent, devPubkey, request.AuthKey)
}

func validateExecuteRequest(request *ChatRequest, transferPubKey string, executorAddress string, transferSignature string) error {
	components := calculations.SignatureComponents{
		Payload:         string(request.Body),
		Timestamp:       request.Timestamp,
		TransferAddress: request.TransferAddress,
		ExecutorAddress: executorAddress,
	}
	return calculations.ValidateSignature(components, calculations.ExecutorAgent, transferPubKey, transferSignature)
}

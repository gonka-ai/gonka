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

func validateExecuteRequestWithGrantees(request *ChatRequest, transferPubkeys []string, executorAddress string, transferSignature string) error {
	components := calculations.SignatureComponents{
		Payload:         string(request.Body),
		Timestamp:       request.Timestamp,
		TransferAddress: request.TransferAddress,
		ExecutorAddress: executorAddress,
	}
	return calculations.ValidateSignatureWithGrantees(components, calculations.ExecutorAgent, transferPubkeys, transferSignature)
}

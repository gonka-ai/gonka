package public

import (
	"decentralized-api/utils"

	"github.com/productscience/inference/x/inference/calculations"
)

// validateTransferRequest validates user signature against original_prompt_hash.
// User signs: hash(original_prompt) + timestamp + ta_address
func validateTransferRequest(request *ChatRequest, devPubkey string) error {
	originalPromptHash := utils.GenerateSHA256Hash(string(request.Body))
	components := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       request.Timestamp,
		TransferAddress: request.TransferAddress,
		ExecutorAddress: "",
	}
	return calculations.ValidateSignature(components, calculations.Developer, devPubkey, request.AuthKey)
}

// validateExecuteRequestWithGrantees validates TA signature against prompt_hash.
// TA signs: hash(prompt_payload) + timestamp + ta_address + executor_address
func validateExecuteRequestWithGrantees(request *ChatRequest, transferPubkeys []string, executorAddress string, transferSignature string) error {
	// Use prompt_hash from header; fallback to computed hash for direct executor flow
	payload := request.PromptHash
	if payload == "" {
		payload = utils.GenerateSHA256Hash(string(request.Body))
	}
	components := calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       request.Timestamp,
		TransferAddress: request.TransferAddress,
		ExecutorAddress: executorAddress,
	}
	return calculations.ValidateSignatureWithGrantees(components, calculations.TransferAgent, transferPubkeys, transferSignature)
}

package payloadstorage

import (
	"decentralized-api/completionapi"
	"decentralized-api/utils"
)

// Matches getPromptHash in post_chat_handler.go
func ComputePromptHash(promptPayload []byte) (string, error) {
	canonical, err := utils.CanonicalizeJSON([]byte(promptPayload))
	if err != nil {
		return "", err
	}
	return utils.GenerateSHA256Hash(canonical), nil
}

// Hashes message content only, not full JSON
func ComputeResponseHash(responsePayload []byte) (string, error) {
	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		return "", err
	}
	return resp.GetHash()
}

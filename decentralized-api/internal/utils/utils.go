package utils

import (
	"decentralized-api/completionapi"
	"decentralized-api/utils"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

// UnquoteEventValue removes JSON quotes from event values
// Cosmos SDK events often have JSON-encoded values like "\"1\"" which need to be unquoted to "1"
func UnquoteEventValue(value string) (string, error) {
	var unquoted string
	err := json.Unmarshal([]byte(value), &unquoted)
	if err != nil {
		return value, nil // Return original value if unquoting fails
	}
	return unquoted, nil
}

// DecodeBase64IfPossible attempts to decode a string as base64
// Returns the decoded bytes if successful, or an error if not valid base64
func DecodeBase64IfPossible(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// DecodeHex decodes a hex string to bytes
// Returns the decoded bytes if successful, or an error if not valid hex
func DecodeHex(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

func GetResponseHash(bodyBytes []byte) (string, *completionapi.Response, error) {
	if (bodyBytes == nil) || (len(bodyBytes) == 0) {
		return "", nil, nil
	}
	var response completionapi.Response
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", nil, err
	}

	// Hash full bytes to include logprobs, preventing manipulation attacks
	hash := utils.GenerateSHA256Hash(string(bodyBytes))
	return hash, &response, nil
}

package streamevidence

import (
	"encoding/json"

	"common/completionapi"
)

// Payload serializes evidence lines into the response payload of an error-miss claim, whose hash a verifier compares with the hashes in the Finish transaction.
func Payload(lines []string) ([]byte, error) {
	return json.Marshal(completionapi.SerializedStreamedResponse{Events: lines})
}

// ProvesErrorMiss reports whether payload passes the check a verifier runs on an error-miss claim's response payload.
func ProvesErrorMiss(payload []byte) bool {
	_, isError := completionapi.IsTerminalErrorResponse(payload)
	return isError
}

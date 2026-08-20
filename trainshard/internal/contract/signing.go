package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signingVersion = "trainshard-http-v0"

func SigningPayload(method, path, timestamp, requestID string, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		signingVersion,
		strings.ToUpper(method),
		path,
		timestamp,
		requestID,
		hex.EncodeToString(digest[:]),
	}, "\n"))
}

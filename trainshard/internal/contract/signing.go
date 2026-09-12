package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signingVersion = "trainshard-http-v0"

// SigningPayload names the host the request was meant for, so a signature made for one
// participant cannot be replayed at another. The audience never travels: each side puts in the
// address it already knows
func SigningPayload(audience, method, path, query, timestamp, requestID string, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		signingVersion,
		audience,
		strings.ToUpper(method),
		path,
		query,
		timestamp,
		requestID,
		hex.EncodeToString(digest[:]),
	}, "\n"))
}

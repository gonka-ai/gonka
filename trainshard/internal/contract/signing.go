package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signingVersion = "trainshard-http-v0"

// the audience never travels, each side puts in the participant it knows, so a request signed for
// one host does not verify at another; changing the payload is a wire break, bump signingVersion
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

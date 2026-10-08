package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const signingVersion = "trainshard-http-v1"

// the audience never travels, each side puts in the participant and the endpoint it knows, so a
// request signed for one machine does not verify at another, even of the same participant;
// changing the payload is a wire break, bump signingVersion
func SigningPayload(participant, endpoint, method, path, query, timestamp, requestID string, body []byte) []byte {
	digest := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		signingVersion,
		participant,
		endpoint,
		strings.ToUpper(method),
		path,
		query,
		timestamp,
		requestID,
		hex.EncodeToString(digest[:]),
	}, "\n"))
}

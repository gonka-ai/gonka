package inference

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// maxSessionIDLength bounds the gateway-supplied session id; a longer one is dropped.
const maxSessionIDLength = 512

// cacheSaltField is vLLM's knob: prefix-cache blocks are shared only between requests with a matching salt.
const cacheSaltField = "cache_salt"

var sessionIDTooLongOnce sync.Once

// sessionIDWithinBound reports whether a gateway session id may key a cache namespace.
func sessionIDWithinBound(sessionID string) bool {
	if len(sessionID) <= maxSessionIDLength {
		return true
	}
	sessionIDTooLongOnce.Do(func() {
		logging.Warn("Dropping session id longer than the bound", types.Inferences, "length", len(sessionID), "max", maxSessionIDLength)
	})
	return false
}

// withCacheSalt stamps the request's cache namespace, scoped to its escrow. An unparseable body passes through.
func withCacheSalt(body []byte, escrowID, sessionID string) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return body
	}
	digest := sha256.Sum256([]byte(escrowID + "\x00" + sessionID))
	fields[cacheSaltField] = json.RawMessage(`"` + hex.EncodeToString(digest[:]) + `"`)
	salted, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return salted
}

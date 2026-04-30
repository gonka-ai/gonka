package server

import "fmt"

// PayloadKey creates a namespaced storage key for devshard payloads.
func PayloadKey(escrowID string, inferenceID uint64) string {
	return fmt.Sprintf("devshard:%s:%d", escrowID, inferenceID)
}

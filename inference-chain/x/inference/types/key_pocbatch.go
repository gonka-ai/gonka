package types

import (
	"github.com/google/uuid"
)

// Deprecated: raw KV key helpers removed in favor of collections.
// Only GenerateBatchID remains for external use.
func GenerateBatchID() string {
	return uuid.New().String()
}

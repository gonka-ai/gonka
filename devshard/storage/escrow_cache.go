package storage

import (
	"encoding/json"
	"fmt"
	"time"
)

// stampEscrowCache sets the write time so readers can bound how long a row may
// stand in for the chain. Stores call this on every Put.
func stampEscrowCache(info EscrowCacheInfo) EscrowCacheInfo {
	info.CachedAt = escrowCacheNowUnix()
	return info
}

func marshalEscrowCache(info EscrowCacheInfo) (string, error) {
	if info.EscrowID == "" {
		return "", fmt.Errorf("escrow cache: empty escrow_id")
	}
	data, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("marshal escrow cache: %w", err)
	}
	return string(data), nil
}

func unmarshalEscrowCache(raw string) (*EscrowCacheInfo, error) {
	var info EscrowCacheInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return nil, fmt.Errorf("unmarshal escrow cache: %w", err)
	}
	if info.EscrowID == "" {
		return nil, fmt.Errorf("escrow cache: empty escrow_id in payload")
	}
	return &info, nil
}

func escrowCacheNowUnix() int64 {
	return time.Now().Unix()
}

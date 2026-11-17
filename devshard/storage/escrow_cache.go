package storage

import (
	"encoding/json"
	"fmt"
	"time"
)

// EscrowCacheMaxAge is how long a warm row may stand in for a live GetEscrow.
// Settlement deletes the row; a host that missed that event must not serve an
// arbitrarily old "open" snapshot while the chain is unreachable.
const EscrowCacheMaxAge = time.Hour

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

func cloneSlotURLs(urls map[string]string) map[string]string {
	if urls == nil {
		return nil
	}
	out := make(map[string]string, len(urls))
	for k, v := range urls {
		out[k] = v
	}
	return out
}

// EscrowCacheFresh reports whether CachedAt is set and still within MaxAge.
func EscrowCacheFresh(cached *EscrowCacheInfo, now time.Time) bool {
	if cached == nil || cached.CachedAt <= 0 {
		return false
	}
	age := now.Sub(time.Unix(cached.CachedAt, 0))
	return age >= 0 && age <= EscrowCacheMaxAge
}

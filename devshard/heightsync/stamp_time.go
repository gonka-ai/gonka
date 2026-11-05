package heightsync

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"devshard/chainoracle/blocks/headertime"
)

// ErrStampTimeMismatch is a compose/apply reject: the local map already has
// (height, hash) and its time disagrees with the stamp time about to be used.
var ErrStampTimeMismatch = errors.New("height-sync stamp time disagrees with the local header map")

// StoredStampTime is a non-fetching map read. *headertime.Source implements it
// via StoredAt. A miss stays a miss.
type StoredStampTime interface {
	StoredAt(height uint64) (headertime.Record, bool)
}

// ResolveObservedTimeMs picks the observed_time_ms for a height stamp under the
// optimistic map rule:
//
//   - Map has (height, matching hash) with time_ms > 0: use that time. If
//     candidate > 0 and differs, return ErrStampTimeMismatch (reject).
//   - Map miss, hash disagree, or non-positive map time: do not block; return
//     candidate (may be 0). Never waits and never fetches.
func ResolveObservedTimeMs(src StoredStampTime, height uint64, stampHash []byte, candidate int64) (int64, error) {
	if height == 0 || src == nil {
		return candidate, nil
	}
	row, ok := src.StoredAt(height)
	if !ok || row.TimeMs <= 0 {
		return candidate, nil
	}
	if len(stampHash) > 0 && len(row.Hash) > 0 && !bytes.Equal(stampHash, row.Hash) {
		return candidate, nil
	}
	if candidate > 0 && candidate != row.TimeMs {
		return 0, fmt.Errorf("%w: height %d map=%d stamp=%d", ErrStampTimeMismatch, height, row.TimeMs, candidate)
	}
	return row.TimeMs, nil
}

// HeaderTimeMs converts a block header time to stamp milliseconds. Zero time yields 0.
func HeaderTimeMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	ms := t.UnixMilli()
	if ms <= 0 {
		return 0
	}
	return ms
}

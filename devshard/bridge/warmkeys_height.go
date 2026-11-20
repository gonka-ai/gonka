package bridge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type WarmKeyHeightFetcher interface {
	FetchWarmKeyLogWithHeight(context.Context, string) ([]WarmKeyEntry, int64, error)
}

type WarmKeyHeightSource interface {
	WarmKeyEntryAtHeight(granter string, id uint64, height int64) (WarmKeyEntry, bool, error)
}

// WarmKeyLogHeightSource exposes a complete granter log at a verified height.
type WarmKeyLogHeightSource interface {
	WarmKeyLogAtHeight(granter string, height int64) (map[uint64]WarmKeyEntry, error)
}

var ErrWarmKeyViewBehind = errors.New("warm-key chain view has not reached the verified diff height")

// LiveAtHeight answers a historical fact, including after replacement/revoke
// or expiration. A later query must give the same answer for an earlier H.
func (e WarmKeyEntry) LiveAtHeight(height int64, blockTime time.Time) bool {
	if height <= 0 || e.GrantedAtHeight <= 0 || e.GrantedAtHeight > height || blockTime.IsZero() {
		return false
	}
	if e.RevokedAtHeight != 0 && e.RevokedAtHeight <= height {
		return false
	}
	if e.End != "" && e.RevokedAtHeight == 0 {
		return false
	} // malformed ended entry
	return e.ExpiresAtUnix == 0 || !blockTime.After(time.Unix(0, e.ExpiresAtUnix))
}

func CheckWarmKeyAtHeight(src WarmKeyGrantSource, cold, warm string, id uint64, height int64, blockTime time.Time) (bool, error) {
	historical, ok := src.(WarmKeyHeightSource)
	if !ok {
		return false, fmt.Errorf("%w: no height-aware source", ErrWarmKeyViewBehind)
	}
	e, found, err := historical.WarmKeyEntryAtHeight(cold, id, height)
	if err != nil || !found {
		return false, err
	}
	if e.Granter != cold || e.ID != id || e.Grantee != warm {
		return false, nil
	}
	return e.LiveAtHeight(height, blockTime), nil
}

// WarmKeyEntryAtHeight uses the authoritative granter log and the trusted
// feed only through height. A later revoke is evaluated against height by
// the caller.
func (c *WarmKeyLogCopy) WarmKeyEntryAtHeight(granter string, id uint64, height int64) (WarmKeyEntry, bool, error) {
	entries, err := c.WarmKeyLogAtHeight(granter, height)
	if err != nil {
		return WarmKeyEntry{}, false, err
	}
	entry, found := entries[id]
	return entry, found, nil
}

func (c *WarmKeyLogCopy) WarmKeyLogAtHeight(granter string, height int64) (map[uint64]WarmKeyEntry, error) {
	if height <= 0 {
		return nil, ErrWarmKeyViewBehind
	}
	read := func() (map[uint64]WarmKeyEntry, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		g := c.granters[granter]
		if !c.coverageLocked(g, height) {
			return nil, false
		}
		entries := c.historicalLocked(granter, g, height)
		for _, e := range entries {
			if e.GrantedAtHeight <= 0 {
				// An end without the original grant is not a complete entry.
				return nil, false
			}
		}
		return entries, true
	}
	if entries, complete := read(); complete {
		return entries, nil
	}
	if _, ok := c.fetch.(WarmKeyHeightFetcher); !ok {
		return nil, ErrWarmKeyViewBehind
	}
	if err := c.load(granter, false); err != nil {
		return nil, err
	}
	if entries, complete := read(); complete {
		return entries, nil
	}
	return nil, fmt.Errorf("%w: %s needs height %d", ErrWarmKeyViewBehind, granter, height)
}

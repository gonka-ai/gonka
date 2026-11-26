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

// WarmKeyEntryAtHeight uses the latest append-only log, but only after a full
// response proves the node has applied at least H. This avoids historical
// store/pruning dependencies without mistaking a lagging node for no revoke.
// A single granter query is shared by its slots, keys and concurrent callers.
func (c *WarmKeyLogCopy) WarmKeyEntryAtHeight(granter string, id uint64, height int64) (WarmKeyEntry, bool, error) {
	if height <= 0 {
		return WarmKeyEntry{}, false, ErrWarmKeyViewBehind
	}
	read := func() (WarmKeyEntry, bool, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		g := c.granters[granter]
		if g == nil || !g.complete || g.confirmedHeight < height {
			return WarmKeyEntry{}, false, false
		}
		e, found := g.entries[id]
		if found && e.GrantedAtHeight <= 0 {
			// An end event may arrive before its grant. Even a covered log
			// height does not make this partial tombstone a complete entry.
			return WarmKeyEntry{}, false, false
		}
		return e, found, true
	}
	if e, found, complete := read(); complete {
		return e, found, nil
	}
	if _, ok := c.fetch.(WarmKeyHeightFetcher); !ok {
		return WarmKeyEntry{}, false, ErrWarmKeyViewBehind
	}
	if err := c.load(granter, false); err != nil {
		return WarmKeyEntry{}, false, err
	}
	if e, found, complete := read(); complete {
		return e, found, nil
	}
	return WarmKeyEntry{}, false, fmt.Errorf("%w: %s needs height %d", ErrWarmKeyViewBehind, granter, height)
}

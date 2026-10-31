package testutil

import (
	"sort"
	"sync"
	"time"

	"devshard/bridge"
	"devshard/types"
)

// WarmKeyBindTx is a MsgWarmKeyBind for tests.
func WarmKeyBindTx(slot uint32, addr string, grantID uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_WarmKeyBind{WarmKeyBind: &types.MsgWarmKeyBind{
		SlotId: slot, Address: addr, GrantId: grantID,
	}}}
}

// WarmKeyRevokeTx is a MsgWarmKeyRevoke for tests.
func WarmKeyRevokeTx(slot uint32, addr string, grantID uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_WarmKeyRevoke{WarmKeyRevoke: &types.MsgWarmKeyRevoke{
		SlotId: slot, Address: addr, GrantId: grantID,
	}}}
}

// WarmKeyGrants is an in-memory warm-key log for tests: a
// bridge.WarmKeyGrantSource whose entries the test grants and revokes
// directly, with ids assigned per granter in grant order like the chain.
type WarmKeyGrants struct {
	mu      sync.Mutex
	entries map[string][]bridge.WarmKeyEntry
	height  int64
	// Err, when set, is returned by every lookup: the chain is unreachable.
	Err error
}

// NewWarmKeyGrants returns an empty log.
func NewWarmKeyGrants() *WarmKeyGrants {
	return &WarmKeyGrants{entries: make(map[string][]bridge.WarmKeyEntry), height: 1}
}

// Grant appends a live entry of warm under cold and returns its id.
func (g *WarmKeyGrants) Grant(cold, warm string) uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.height++
	id := uint64(len(g.entries[cold]) + 1)
	g.entries[cold] = append(g.entries[cold], bridge.WarmKeyEntry{
		Granter: cold, ID: id, Grantee: warm, GrantedAtHeight: g.height,
	})
	return id
}

// Revoke ends every live entry of warm under cold.
func (g *WarmKeyGrants) Revoke(cold, warm string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.height++
	for i := range g.entries[cold] {
		e := &g.entries[cold][i]
		if e.Grantee == warm && !e.Ended() {
			e.RevokedAtHeight = g.height
			e.End = "WARM_KEY_LOG_END_REVOKED"
		}
	}
}

// LiveWarmKeys implements bridge.WarmKeyGrantSource.
func (g *WarmKeyGrants) LiveWarmKeys(cold string) ([]bridge.WarmKeyEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Err != nil {
		return nil, g.Err
	}
	var out []bridge.WarmKeyEntry
	for _, e := range g.entries[cold] {
		if e.LiveAt(time.Now()) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// WarmKeyEntry implements bridge.WarmKeyGrantSource.
func (g *WarmKeyGrants) WarmKeyEntry(cold string, id uint64) (bridge.WarmKeyEntry, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Err != nil {
		return bridge.WarmKeyEntry{}, false, g.Err
	}
	for _, e := range g.entries[cold] {
		if e.ID == id {
			return e, true, nil
		}
	}
	return bridge.WarmKeyEntry{}, false, nil
}

// Resolver is the grant source as a state.WarmKeyResolver: live grant or not.
func (g *WarmKeyGrants) Resolver(warm, cold string) (bool, error) {
	return bridge.IsWarmKeyLive(g, cold, warm, time.Now())
}

// Confirmer is the grant source as a state.WarmKeyConfirmer.
func (g *WarmKeyGrants) Confirmer(cold, warm string, id uint64) error {
	return bridge.ConfirmWarmKeyBind(g, cold, warm, id)
}

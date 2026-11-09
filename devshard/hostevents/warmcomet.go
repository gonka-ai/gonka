package hostevents

import (
	"strconv"

	"common/chainoracle/blocks/observer"
	"devshard/bridge"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// Queries registered on the process's existing Comet socket. A replace emits
// the revoke and the grant in one tx, so each delivery is parsed in full.
const (
	WarmKeyGrantedQuery = "tm.event='Tx' AND warm_key_granted.grant_id EXISTS"
	WarmKeyRevokedQuery = "tm.event='Tx' AND warm_key_revoked.grant_id EXISTS"
)

const (
	warmKeyGrantedType = "warm_key_granted"
	warmKeyRevokedType = "warm_key_revoked"
	warmKeyEndRevoked  = "WARM_KEY_LOG_END_REVOKED"
)

// WarmKeyQueries is the pair added to the socket heights already use.
func WarmKeyQueries() []string {
	return []string{WarmKeyGrantedQuery, WarmKeyRevokedQuery}
}

// WarmKeysFromResult reads every warm-key ABCI event in one Comet delivery,
// in order. A tx delivery carries grants and revokes. A NewBlock delivery
// carries EndBlock expiries on ResultFinalizeBlock. It does not query the
// chain.
func WarmKeysFromResult(result ctypes.ResultEvent) []bridge.WarmKeyEntry {
	switch data := result.Data.(type) {
	case cmttypes.EventDataTx:
		return warmKeysFromTx(data)
	case *cmttypes.EventDataTx:
		if data == nil {
			return nil
		}
		return warmKeysFromTx(*data)
	default:
		block, ok := observer.AsEventDataNewBlock(result.Data)
		if !ok {
			return nil
		}
		height := int64(0)
		if block.Block != nil {
			height = block.Block.Height
		}
		return warmKeysFromABCI(block.ResultFinalizeBlock.Events, height)
	}
}

func warmKeysFromTx(data cmttypes.EventDataTx) []bridge.WarmKeyEntry {
	if data.TxResult.Result.Code != 0 {
		return nil
	}
	return warmKeysFromABCI(data.TxResult.Result.Events, data.TxResult.Height)
}

// WarmKeysFromABCI reads warm-key events from a NewBlock finalize-block slice.
// height is that block's height. An event that carries its own height attribute
// keeps that value.
func WarmKeysFromABCI(events []abci.Event, height int64) []bridge.WarmKeyEntry {
	return warmKeysFromABCI(events, height)
}

func warmKeysFromABCI(events []abci.Event, txHeight int64) []bridge.WarmKeyEntry {
	if len(events) == 0 {
		return nil
	}
	out := make([]bridge.WarmKeyEntry, 0, len(events))
	for _, ev := range events {
		entry, ok := warmEntryFromABCI(ev, txHeight)
		if ok {
			out = append(out, entry)
		}
	}
	return out
}

func warmEntryFromABCI(ev abci.Event, txHeight int64) (bridge.WarmKeyEntry, bool) {
	switch ev.Type {
	case warmKeyGrantedType, warmKeyRevokedType:
	default:
		return bridge.WarmKeyEntry{}, false
	}
	granter := abciAttr(ev, "granter")
	grantee := abciAttr(ev, "grantee")
	id, err := strconv.ParseUint(abciAttr(ev, "grant_id"), 10, 64)
	if err != nil || granter == "" || grantee == "" || id == 0 {
		return bridge.WarmKeyEntry{}, false
	}
	height := txHeight
	if raw := abciAttr(ev, "height"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return bridge.WarmKeyEntry{}, false
		}
		height = parsed
	}
	entry := bridge.WarmKeyEntry{
		Granter: granter,
		ID:      id,
		Grantee: grantee,
	}
	if raw := abciAttr(ev, "expires_at"); raw != "" {
		expires, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return bridge.WarmKeyEntry{}, false
		}
		entry.ExpiresAtUnix = expires
	}
	if ev.Type == warmKeyRevokedType {
		entry.RevokedAtHeight = height
		entry.End = abciAttr(ev, "end")
		if entry.End == "" {
			entry.End = warmKeyEndRevoked
		}
		return entry, true
	}
	entry.GrantedAtHeight = height
	return entry, true
}

func abciAttr(ev abci.Event, key string) string {
	for _, a := range ev.Attributes {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

package heightsync

import (
	"context"

	"common/chainoracle/blocks"
	"devshard/types"
)

type staleChecker interface{ Stale() bool }

func oracleStale(oracle blocks.BlockOracle) bool {
	if so, ok := oracle.(staleChecker); ok {
		return so.Stale()
	}
	return false
}

// EvaluateSyncState is the spec §11.2 table as one pure function.
// CATCHING_UP is reported; requiring Strong on the next heartbeat is spec §8 / §15.
// Hash-only oracles (empty Commit) are enough for SYNCED.
func EvaluateSyncState(ctx context.Context, oracle blocks.BlockOracle, hRef uint64, cfg HeartbeatConfig) types.SyncState {
	cfg = cfg.withDefaults()
	if oracle == nil {
		return types.SyncState_ORACLE_UNAVAILABLE
	}
	hdr, err := oracle.Latest(ctx)
	return evaluateSyncState(oracleStale(oracle), hdr, err, hRef, cfg)
}

// EvaluateSyncStateFromHeader is EvaluateSyncState using an already-fetched
// header so the ack stamp matches the response-leg Anchor of this exchange.
func EvaluateSyncStateFromHeader(oracle blocks.BlockOracle, hdr *blocks.Header, latestErr error, hRef uint64, cfg HeartbeatConfig) types.SyncState {
	cfg = cfg.withDefaults()
	if oracle == nil {
		return types.SyncState_ORACLE_UNAVAILABLE
	}
	return evaluateSyncState(oracleStale(oracle), hdr, latestErr, hRef, cfg)
}

func evaluateSyncState(stale bool, hdr *blocks.Header, latestErr error, hRef uint64, cfg HeartbeatConfig) types.SyncState {
	if latestErr != nil || hdr == nil {
		return types.SyncState_ORACLE_UNAVAILABLE
	}
	if stale {
		return types.SyncState_ORACLE_STALE
	}
	local := uint64(hdr.Height)
	var delta uint64
	if hRef > local {
		delta = hRef - local
	} else {
		delta = local - hRef
	}
	if delta > cfg.DeltaBlocks {
		return types.SyncState_CATCHING_UP
	}
	return types.SyncState_SYNCED
}

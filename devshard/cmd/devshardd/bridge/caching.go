package bridge

import (
	"log/slog"

	"devshard/bridge"
	"devshard/storage"
)

// EscrowCacheStore is the storage subset the caching bridge needs to read the
// escrow metadata prefetched by the host-events long-poll warm (PR #1443).
type EscrowCacheStore interface {
	GetEscrowCache(escrowID string) (*storage.EscrowCacheInfo, error)
}

// CachingEscrowBridge wraps a MainnetBridge so GetEscrow falls back to the local
// escrow_cache when the live chain query fails. The cache is populated ahead of
// time by the escrow long-poll warm sink, so a first inference bind can still
// succeed when the request-time escrow fetch path is unavailable.
//
// All other MainnetBridge methods are inherited unchanged from the embedded
// bridge. Only GetEscrow is cache-aware.
type CachingEscrowBridge struct {
	bridge.MainnetBridge
	store EscrowCacheStore
	log   *slog.Logger
}

// NewCachingEscrowBridge wraps inner with escrow_cache fallback. store may be nil
// (fallback disabled), in which case GetEscrow behaves exactly like inner.
func NewCachingEscrowBridge(inner bridge.MainnetBridge, store EscrowCacheStore, log *slog.Logger) *CachingEscrowBridge {
	if log == nil {
		log = slog.Default()
	}
	return &CachingEscrowBridge{MainnetBridge: inner, store: store, log: log}
}

// GetEscrow queries the chain first (source of truth). If that fails and a warm
// cache row exists, it serves the cached metadata instead, returning the live
// error only when the cache also has nothing.
func (b *CachingEscrowBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	info, err := b.MainnetBridge.GetEscrow(escrowID)
	if err == nil {
		return info, nil
	}
	if b.store == nil {
		return nil, err
	}
	cached, cacheErr := b.store.GetEscrowCache(escrowID)
	if cacheErr != nil || cached == nil {
		return nil, err
	}
	b.log.Info("escrow: serving from long-poll warm cache (live fetch failed)",
		"escrow_id", escrowID, "live_err", err)
	return EscrowInfoFromCache(cached), nil
}

// EscrowCacheFromInfo maps a chain-fetched EscrowInfo to the storable cache row.
func EscrowCacheFromInfo(e *bridge.EscrowInfo) storage.EscrowCacheInfo {
	return storage.EscrowCacheInfo{
		EscrowID:                  e.EscrowID,
		Amount:                    e.Amount,
		CreatorAddress:            e.CreatorAddress,
		AppHash:                   e.AppHash,
		Slots:                     e.Slots,
		TokenPrice:                e.TokenPrice,
		CreateDevshardFee:         e.CreateDevshardFee,
		FeePerNonce:               e.FeePerNonce,
		InferenceSealGraceNonces:  e.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: e.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      e.AutoSealEveryNNonces,
		ValidationRate:            e.ValidationRate,
		VoteThresholdFactor:       e.VoteThresholdFactor,
		LogprobsMode:              e.LogprobsMode,
		EpochID:                   e.EpochID,
	}
}

// EscrowInfoFromCache maps a stored cache row back to a bridge EscrowInfo.
func EscrowInfoFromCache(c *storage.EscrowCacheInfo) *bridge.EscrowInfo {
	return &bridge.EscrowInfo{
		EscrowID:                  c.EscrowID,
		Amount:                    c.Amount,
		CreatorAddress:            c.CreatorAddress,
		AppHash:                   c.AppHash,
		Slots:                     c.Slots,
		TokenPrice:                c.TokenPrice,
		CreateDevshardFee:         c.CreateDevshardFee,
		FeePerNonce:               c.FeePerNonce,
		InferenceSealGraceNonces:  c.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: c.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      c.AutoSealEveryNNonces,
		ValidationRate:            c.ValidationRate,
		VoteThresholdFactor:       c.VoteThresholdFactor,
		LogprobsMode:              c.LogprobsMode,
		EpochID:                   c.EpochID,
	}
}

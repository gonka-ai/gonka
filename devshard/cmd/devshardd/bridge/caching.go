package bridge

import (
	"log/slog"
	"time"

	devshardpkg "devshard"
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
	ttl   time.Duration
	now   func() time.Time
}

// DefaultEscrowCacheTTL bounds how long a warm row may stand in for the chain.
// A row is only refreshed by an escrow-created event, and it is deleted on
// settlement, so a host that missed the settled event would otherwise serve an
// arbitrarily old "open" row for as long as the chain stays unreachable. One
// hour keeps the fallback useful across a chain outage while capping how far
// behind chain truth a bind can be.
const DefaultEscrowCacheTTL = time.Hour

// NewCachingEscrowBridge wraps inner with escrow_cache fallback. store may be nil
// (fallback disabled), in which case GetEscrow behaves exactly like inner.
func NewCachingEscrowBridge(inner bridge.MainnetBridge, store EscrowCacheStore, log *slog.Logger) *CachingEscrowBridge {
	if log == nil {
		log = slog.Default()
	}
	return &CachingEscrowBridge{
		MainnetBridge: inner,
		store:         store,
		log:           log,
		ttl:           DefaultEscrowCacheTTL,
		now:           time.Now,
	}
}

// GetEscrow queries the chain first (source of truth). If that fails and a warm
// cache row exists and is still within the TTL, it serves the cached metadata
// instead, returning the live error otherwise.
func (b *CachingEscrowBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	if err := devshardpkg.ValidateEscrowID(escrowID); err != nil {
		return nil, err
	}
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
	if age, stale := b.staleness(cached); stale {
		// Settlement deletes the row, so a stale row means we have not heard
		// from the chain in a long time and cannot tell open from settled.
		// Refusing keeps a settled escrow from serving inference during an
		// outage; the caller still sees the live error and can retry.
		b.log.Warn("escrow: warm cache row too old to stand in for chain, refusing fallback",
			"escrow_id", escrowID, "age", age, "ttl", b.ttl, "live_err", err)
		return nil, err
	}
	b.log.Info("escrow: serving from long-poll warm cache (live fetch failed)",
		"escrow_id", escrowID, "live_err", err)
	return EscrowInfoFromCache(cached), nil
}

// staleness reports the row age and whether it exceeds the TTL. Rows written
// before CachedAt existed report zero and are always stale.
func (b *CachingEscrowBridge) staleness(cached *storage.EscrowCacheInfo) (time.Duration, bool) {
	if b.ttl <= 0 {
		return 0, false
	}
	if cached.CachedAt <= 0 {
		return 0, true
	}
	age := b.clock().Sub(time.Unix(cached.CachedAt, 0))
	return age, age > b.ttl
}

func (b *CachingEscrowBridge) clock() time.Time {
	if b.now == nil {
		return time.Now()
	}
	return b.now()
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
		EpochID:                   c.EpochID,
	}
}

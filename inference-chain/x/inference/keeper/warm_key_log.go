package keeper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	"github.com/productscience/inference/x/inference/types"
)

// The warm-key log is the chain's append-only record of warm-key grants.
//
// devshard binds a host's warm key to a slot by naming a log entry id in a
// diff, and every host confirms the entry against this log. The log answers
// the same way for ever, so hosts that ask at different times, or replay old
// diffs long after a revoke, reach the same key state. Entries are created
// when a grant of the marker (types.WarmKeyGrantMarkerTypeURL) appears and
// ended, never deleted, when it is revoked or replaced. A grant's expiration
// is stored on the entry and is not a chain event: the module does not walk
// live keys in EndBlock and does not emit warm_key_revoked when the time passes.
//
// Writers: the post handler in app (MsgGrant, MsgRevoke, nested in MsgExec)
// and SeedWarmKeyLog from the migration that introduces the log.

// Events emitted by the log. decentralized-api forwards them to devshard
// hosts as host events.
const (
	EventTypeWarmKeyGranted = "warm_key_granted"
	EventTypeWarmKeyRevoked = "warm_key_revoked"

	AttributeKeyWarmKeyGranter   = "granter"
	AttributeKeyWarmKeyGrantee   = "grantee"
	AttributeKeyWarmKeyGrantID   = "grant_id"
	AttributeKeyWarmKeyHeight    = "height"
	AttributeKeyWarmKeyExpiresAt = "expires_at"
	AttributeKeyWarmKeyEnd       = "end"
)

// IsWarmKeyMarker reports whether msgTypeURL is the warm-key grant marker.
func IsWarmKeyMarker(msgTypeURL string) bool {
	return strings.TrimPrefix(msgTypeURL, "/") == strings.TrimPrefix(types.WarmKeyGrantMarkerTypeURL, "/")
}

// RecordWarmKeyGrant appends a live entry for a grant of the marker from
// granter to grantee at the current height. A live entry for the same pair is
// ended as REPLACED first: authz keeps one grant per (granter, grantee,
// message type), so a second MsgGrant overwrites the first, and the log keeps
// both with the new one ordered after the old by id. An index row whose grant
// already expired is dropped and not ended; the new grant is the next id, as
// if the pair had no open entry.
func (k Keeper) RecordWarmKeyGrant(ctx context.Context, granter, grantee sdk.AccAddress, expiration *time.Time) (types.WarmKeyLogEntry, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	if live, found, err := k.liveWarmKeyEntry(ctx, granter, grantee); err != nil {
		return types.WarmKeyLogEntry{}, err
	} else if found && warmKeyExpired(live, sdkCtx.BlockTime().UnixNano()) {
		// The index row outlived the grant. Authz already dropped it. Do not
		// end the log entry as REPLACED and do not emit warm_key_revoked.
		if err := k.WarmKeyLive.Remove(ctx, collections.Join(granter, grantee)); err != nil {
			return types.WarmKeyLogEntry{}, err
		}
	} else if found {
		if err := k.endWarmKeyEntry(ctx, live, height, types.WarmKeyLogEnd_WARM_KEY_LOG_END_REPLACED); err != nil {
			return types.WarmKeyLogEntry{}, err
		}
	}

	nextID, err := k.WarmKeyLogNextID.Get(ctx, granter)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return types.WarmKeyLogEntry{}, err
		}
		nextID = 1
	}
	entry := types.WarmKeyLogEntry{
		Granter:         granter.String(),
		Id:              nextID,
		Grantee:         grantee.String(),
		GrantedAtHeight: height,
	}
	if expiration != nil {
		entry.ExpiresAtUnix = expiration.UnixNano()
	}
	if err := k.WarmKeyLogEntriesMap.Set(ctx, collections.Join(granter, nextID), entry); err != nil {
		return types.WarmKeyLogEntry{}, err
	}
	if err := k.WarmKeyLogNextID.Set(ctx, granter, nextID+1); err != nil {
		return types.WarmKeyLogEntry{}, err
	}
	if err := k.WarmKeyLive.Set(ctx, collections.Join(granter, grantee), nextID); err != nil {
		return types.WarmKeyLogEntry{}, err
	}
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		EventTypeWarmKeyGranted,
		sdk.NewAttribute(AttributeKeyWarmKeyGranter, entry.Granter),
		sdk.NewAttribute(AttributeKeyWarmKeyGrantee, entry.Grantee),
		sdk.NewAttribute(AttributeKeyWarmKeyGrantID, fmt.Sprint(entry.Id)),
		sdk.NewAttribute(AttributeKeyWarmKeyHeight, fmt.Sprint(height)),
		sdk.NewAttribute(AttributeKeyWarmKeyExpiresAt, fmt.Sprint(entry.ExpiresAtUnix)),
	))
	return entry, nil
}

// RecordWarmKeyRevoke ends the live entry for (granter, grantee) as REVOKED.
// It reports false, with no error, when the pair has no live entry: a revoke
// of a grant the log never saw (a legacy marker, or a grant from before the
// log existed that the seeding skipped as expired) is not a fault. An index
// row whose grant already expired is cleared and not ended: the timestamp is
// the end, and no warm_key_revoked event is emitted.
func (k Keeper) RecordWarmKeyRevoke(ctx context.Context, granter, grantee sdk.AccAddress) (bool, error) {
	live, found, err := k.liveWarmKeyEntry(ctx, granter, grantee)
	if err != nil || !found {
		return false, err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if warmKeyExpired(live, sdkCtx.BlockTime().UnixNano()) {
		return false, k.WarmKeyLive.Remove(ctx, collections.Join(granter, grantee))
	}
	return true, k.endWarmKeyEntry(ctx, live, sdkCtx.BlockHeight(), types.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED)
}

// warmKeyExpired reports whether the grant's expiration is strictly before now.
// now and ExpiresAtUnix are unix nanoseconds. The boundary matches authz,
// which drops a grant only when Expiration.Before(blockTime).
func warmKeyExpired(entry types.WarmKeyLogEntry, now int64) bool {
	return entry.ExpiresAtUnix != 0 && entry.ExpiresAtUnix < now
}

// SeedWarmKeyLog fills an empty log from the authz grants that exist at the
// moment the log is introduced: one live entry per unexpired grant of the
// marker, granted at the current height, in the order iterate yields them,
// which is store order and therefore the same on every node. It returns the
// number of entries written. A log that already has entries is left alone.
func (k Keeper) SeedWarmKeyLog(ctx context.Context, iterate func(ctx context.Context, handler func(granter, grantee sdk.AccAddress, grant authz.Grant) bool)) (int, error) {
	seeded := false
	if err := k.WarmKeyLogNextID.Walk(ctx, nil, func(sdk.AccAddress, uint64) (bool, error) {
		seeded = true
		return true, nil
	}); err != nil {
		return 0, err
	}
	if seeded {
		k.LogInfo("warm-key log already seeded, skipping", types.Upgrades)
		return 0, nil
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockTime := sdkCtx.BlockTime()
	var (
		count    int
		iterErr  error
		skipped  int
		expiredN int
	)
	iterate(ctx, func(granter, grantee sdk.AccAddress, grant authz.Grant) bool {
		auth, ok := k.genericAuthorization(grant)
		if !ok || !IsWarmKeyMarker(auth.Msg) {
			skipped++
			return false
		}
		if grant.Expiration != nil && !grant.Expiration.After(blockTime) {
			expiredN++
			return false
		}
		if _, err := k.RecordWarmKeyGrant(ctx, granter, grantee, grant.Expiration); err != nil {
			iterErr = fmt.Errorf("seed warm-key log %s -> %s: %w", granter, grantee, err)
			return true
		}
		count++
		return false
	})
	if iterErr != nil {
		return count, iterErr
	}
	k.LogInfo("warm-key log seeded", types.Upgrades, "entries", count, "other_grants", skipped, "expired", expiredN)
	return count, nil
}

// genericAuthorization reads a grant's authorization as a GenericAuthorization:
// from the Any's cached value when the codec unpacked it, otherwise from its
// bytes, as the earlier authz migrations do.
func (k Keeper) genericAuthorization(grant authz.Grant) (*authz.GenericAuthorization, bool) {
	if auth, ok := grant.Authorization.GetCachedValue().(*authz.GenericAuthorization); ok {
		return auth, true
	}
	if grant.Authorization == nil || grant.Authorization.GetTypeUrl() != "/cosmos.authz.v1beta1.GenericAuthorization" {
		return nil, false
	}
	var generic authz.GenericAuthorization
	if err := k.cdc.Unmarshal(grant.Authorization.Value, &generic); err != nil {
		return nil, false
	}
	return &generic, true
}

// WarmKeyLogEntries returns every entry of granter in id order.
func (k Keeper) WarmKeyLogEntries(ctx context.Context, granter sdk.AccAddress) ([]types.WarmKeyLogEntry, error) {
	var out []types.WarmKeyLogEntry
	rng := collections.NewPrefixedPairRange[sdk.AccAddress, uint64](granter)
	err := k.WarmKeyLogEntriesMap.Walk(ctx, rng, func(_ collections.Pair[sdk.AccAddress, uint64], entry types.WarmKeyLogEntry) (bool, error) {
		out = append(out, entry)
		return false, nil
	})
	return out, err
}

func (k Keeper) liveWarmKeyEntry(ctx context.Context, granter, grantee sdk.AccAddress) (types.WarmKeyLogEntry, bool, error) {
	id, err := k.WarmKeyLive.Get(ctx, collections.Join(granter, grantee))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.WarmKeyLogEntry{}, false, nil
		}
		return types.WarmKeyLogEntry{}, false, err
	}
	entry, err := k.WarmKeyLogEntriesMap.Get(ctx, collections.Join(granter, id))
	if err != nil {
		return types.WarmKeyLogEntry{}, false, fmt.Errorf("warm-key live index names missing entry %s/%d: %w", granter, id, err)
	}
	return entry, true, nil
}

func (k Keeper) endWarmKeyEntry(ctx context.Context, entry types.WarmKeyLogEntry, height int64, end types.WarmKeyLogEnd) error {
	granter, err := sdk.AccAddressFromBech32(entry.Granter)
	if err != nil {
		return err
	}
	grantee, err := sdk.AccAddressFromBech32(entry.Grantee)
	if err != nil {
		return err
	}
	entry.RevokedAtHeight = height
	entry.End = end
	if err := k.WarmKeyLogEntriesMap.Set(ctx, collections.Join(granter, entry.Id), entry); err != nil {
		return err
	}
	if err := k.WarmKeyLive.Remove(ctx, collections.Join(granter, grantee)); err != nil {
		return err
	}
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		EventTypeWarmKeyRevoked,
		sdk.NewAttribute(AttributeKeyWarmKeyGranter, entry.Granter),
		sdk.NewAttribute(AttributeKeyWarmKeyGrantee, entry.Grantee),
		sdk.NewAttribute(AttributeKeyWarmKeyGrantID, fmt.Sprint(entry.Id)),
		sdk.NewAttribute(AttributeKeyWarmKeyHeight, fmt.Sprint(height)),
		sdk.NewAttribute(AttributeKeyWarmKeyEnd, end.String()),
	))
	return nil
}

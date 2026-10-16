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
// ended, never deleted, when it is revoked, replaced or expires.
//
// Writers: the post handler in app (MsgGrant, MsgRevoke, nested in MsgExec),
// ExpireWarmKeys from EndBlock, and SeedWarmKeyLog from the migration that
// introduces the log.

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
// both with the new one ordered after the old by id.
func (k Keeper) RecordWarmKeyGrant(ctx context.Context, granter, grantee sdk.AccAddress, expiration *time.Time) (types.WarmKeyLogEntry, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()

	if live, found, err := k.liveWarmKeyEntry(ctx, granter, grantee); err != nil {
		return types.WarmKeyLogEntry{}, err
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
		entry.ExpiresAtUnix = expiration.Unix()
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
// log existed that the seeding skipped as expired) is not a fault.
func (k Keeper) RecordWarmKeyRevoke(ctx context.Context, granter, grantee sdk.AccAddress) (bool, error) {
	live, found, err := k.liveWarmKeyEntry(ctx, granter, grantee)
	if err != nil || !found {
		return false, err
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	return true, k.endWarmKeyEntry(ctx, live, height, types.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED)
}

// ExpireWarmKeys ends every live entry whose expiration is at or before the
// block time. authz prunes the grant itself in its own BeginBlock; this is the
// log's view of the same moment. The live index holds one entry per warm key
// in the network, so the walk is small.
func (k Keeper) ExpireWarmKeys(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	now := sdkCtx.BlockTime().Unix()
	height := sdkCtx.BlockHeight()

	var expired []types.WarmKeyLogEntry
	err := k.WarmKeyLive.Walk(ctx, nil, func(key collections.Pair[sdk.AccAddress, sdk.AccAddress], id uint64) (bool, error) {
		entry, err := k.WarmKeyLogEntriesMap.Get(ctx, collections.Join(key.K1(), id))
		if err != nil {
			return true, err
		}
		if entry.ExpiresAtUnix != 0 && entry.ExpiresAtUnix <= now {
			expired = append(expired, entry)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	for _, entry := range expired {
		if err := k.endWarmKeyEntry(ctx, entry, height, types.WarmKeyLogEnd_WARM_KEY_LOG_END_EXPIRED); err != nil {
			return err
		}
	}
	return nil
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
		auth, ok := grant.Authorization.GetCachedValue().(*authz.GenericAuthorization)
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

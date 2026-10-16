package keeper_test

import (
	"context"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func warmKeyTestAddr(seed byte) sdk.AccAddress {
	b := make([]byte, 20)
	for i := range b {
		b[i] = seed
	}
	return sdk.AccAddress(b)
}

func eventsOfType(ctx sdk.Context, typ string) []sdk.Event {
	var out []sdk.Event
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// Test flow:
//  1. Grant a warm key at height 100, grant the same pair again at height 120, revoke at height 150.
//  2. Assert the log holds entries 1 (replaced at 120) and 2 (revoked at 150), the live index follows, the
//     events carry the ids and ends, and a revoke of a pair with no live entry is a no-op.
func TestWarmKeyLogGrantReplaceRevoke(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter, grantee := warmKeyTestAddr(1), warmKeyTestAddr(2)
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	ctx = ctx.WithBlockHeight(100)
	first, err := k.RecordWarmKeyGrant(ctx, granter, grantee, &expires)
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Id)
	require.Equal(t, int64(100), first.GrantedAtHeight)
	require.Equal(t, expires.Unix(), first.ExpiresAtUnix)
	require.Equal(t, types.WarmKeyLogEnd_WARM_KEY_LOG_END_NONE, first.End)

	ctx = ctx.WithBlockHeight(120)
	second, err := k.RecordWarmKeyGrant(ctx, granter, grantee, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), second.Id)
	require.Zero(t, second.ExpiresAtUnix)

	entries, err := k.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, int64(120), entries[0].RevokedAtHeight)
	require.Equal(t, types.WarmKeyLogEnd_WARM_KEY_LOG_END_REPLACED, entries[0].End)
	require.Zero(t, entries[1].RevokedAtHeight)

	ctx = ctx.WithBlockHeight(150)
	ended, err := k.RecordWarmKeyRevoke(ctx, granter, grantee)
	require.NoError(t, err)
	require.True(t, ended)
	ended, err = k.RecordWarmKeyRevoke(ctx, granter, grantee)
	require.NoError(t, err)
	require.False(t, ended, "no live entry left to end")

	entries, err = k.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Equal(t, int64(150), entries[1].RevokedAtHeight)
	require.Equal(t, types.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED, entries[1].End)

	granted := eventsOfType(ctx, keeper.EventTypeWarmKeyGranted)
	require.Len(t, granted, 2)
	revoked := eventsOfType(ctx, keeper.EventTypeWarmKeyRevoked)
	require.Len(t, revoked, 2)
	require.Equal(t, "1", attr(revoked[0], keeper.AttributeKeyWarmKeyGrantID))
	require.Equal(t, "WARM_KEY_LOG_END_REPLACED", attr(revoked[0], keeper.AttributeKeyWarmKeyEnd))
	require.Equal(t, "2", attr(revoked[1], keeper.AttributeKeyWarmKeyGrantID))
	require.Equal(t, "WARM_KEY_LOG_END_REVOKED", attr(revoked[1], keeper.AttributeKeyWarmKeyEnd))

	// A later grant of the same key starts at id 3: ids never go back.
	third, err := k.RecordWarmKeyGrant(ctx.WithBlockHeight(160), granter, grantee, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(3), third.Id)
}

func attr(ev sdk.Event, key string) string {
	for _, a := range ev.Attributes {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// Test flow:
//  1. Grant one key with an expiration and one without, then run ExpireWarmKeys before and after the expiration.
//  2. Assert the expiring entry ends as EXPIRED at the block where the time passed and the other stays live.
func TestWarmKeyLogExpiresAtBlockTime(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter, timed, forever := warmKeyTestAddr(1), warmKeyTestAddr(2), warmKeyTestAddr(3)
	expires := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	ctx = ctx.WithBlockHeight(10).WithBlockTime(expires.Add(-time.Hour))
	_, err := k.RecordWarmKeyGrant(ctx, granter, timed, &expires)
	require.NoError(t, err)
	_, err = k.RecordWarmKeyGrant(ctx, granter, forever, nil)
	require.NoError(t, err)

	require.NoError(t, k.ExpireWarmKeys(ctx.WithBlockHeight(11).WithBlockTime(expires.Add(-time.Second))))
	entries, err := k.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Zero(t, entries[0].RevokedAtHeight, "not expired yet")

	require.NoError(t, k.ExpireWarmKeys(ctx.WithBlockHeight(12).WithBlockTime(expires)))
	entries, err = k.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Equal(t, int64(12), entries[0].RevokedAtHeight)
	require.Equal(t, types.WarmKeyLogEnd_WARM_KEY_LOG_END_EXPIRED, entries[0].End)
	require.Zero(t, entries[1].RevokedAtHeight, "a grant without expiration never expires")

	ended, err := k.RecordWarmKeyRevoke(ctx, granter, timed)
	require.NoError(t, err)
	require.False(t, ended, "an expired entry is already ended")
}

// Test flow:
//  1. Seed from an iterator that yields a marker grant, a grant of another message type, an expired marker grant
//     and a second marker grant of the same granter, at height 500.
//  2. Assert two entries are written with ids 1 and 2 at height 500, the others are skipped, and a second seed
//     writes nothing.
func TestWarmKeyLogSeedFromAuthzGrants(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter, a, b, c := warmKeyTestAddr(1), warmKeyTestAddr(2), warmKeyTestAddr(3), warmKeyTestAddr(4)
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ctx = ctx.WithBlockHeight(500).WithBlockTime(now)
	past, future := now.Add(-time.Hour), now.Add(24*time.Hour)

	grants := []struct {
		grantee sdk.AccAddress
		msg     string
		exp     *time.Time
	}{
		{a, types.WarmKeyGrantMarkerTypeURL, &future},
		{b, "/cosmos.bank.v1beta1.MsgSend", nil},
		{b, types.WarmKeyGrantMarkerTypeURL, &past},
		{c, types.WarmKeyGrantMarkerTypeURL, nil},
	}
	iterate := func(_ context.Context, handler func(granter, grantee sdk.AccAddress, grant authz.Grant) bool) {
		for _, g := range grants {
			anyAuth, err := codectypes.NewAnyWithValue(authz.NewGenericAuthorization(g.msg))
			require.NoError(t, err)
			if handler(granter, g.grantee, authz.Grant{Authorization: anyAuth, Expiration: g.exp}) {
				return
			}
		}
	}

	n, err := k.SeedWarmKeyLog(ctx, iterate)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	entries, err := k.WarmKeyLogEntries(ctx, granter)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, a.String(), entries[0].Grantee)
	require.Equal(t, future.Unix(), entries[0].ExpiresAtUnix)
	require.Equal(t, c.String(), entries[1].Grantee)
	require.Equal(t, int64(500), entries[1].GrantedAtHeight)

	n, err = k.SeedWarmKeyLog(ctx, iterate)
	require.NoError(t, err)
	require.Zero(t, n, "a seeded log is left alone")
}

// Test flow:
//  1. Record two entries and query the log and single entries.
//  2. Assert the log query lists both in id order, the entry query returns the record, an unknown id is NotFound
//     and bad input is InvalidArgument.
func TestWarmKeyLogQueries(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter, grantee := warmKeyTestAddr(1), warmKeyTestAddr(2)
	ctx = ctx.WithBlockHeight(7)
	_, err := k.RecordWarmKeyGrant(ctx, granter, grantee, nil)
	require.NoError(t, err)
	_, err = k.RecordWarmKeyGrant(ctx, granter, grantee, nil)
	require.NoError(t, err)

	logResp, err := k.WarmKeyLog(ctx, &types.QueryWarmKeyLogRequest{Granter: granter.String()})
	require.NoError(t, err)
	require.Len(t, logResp.Entries, 2)
	require.Equal(t, uint64(1), logResp.Entries[0].Id)
	require.Equal(t, uint64(2), logResp.Entries[1].Id)

	entryResp, err := k.WarmKeyEntry(ctx, &types.QueryWarmKeyEntryRequest{Granter: granter.String(), Id: 1})
	require.NoError(t, err)
	require.Equal(t, types.WarmKeyLogEnd_WARM_KEY_LOG_END_REPLACED, entryResp.Entry.End)

	_, err = k.WarmKeyEntry(ctx, &types.QueryWarmKeyEntryRequest{Granter: granter.String(), Id: 3})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = k.WarmKeyEntry(ctx, &types.QueryWarmKeyEntryRequest{Granter: "nope", Id: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = k.WarmKeyEntry(ctx, &types.QueryWarmKeyEntryRequest{Granter: granter.String(), Id: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = k.WarmKeyLog(ctx, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	empty, err := k.WarmKeyLog(ctx, &types.QueryWarmKeyLogRequest{Granter: warmKeyTestAddr(9).String()})
	require.NoError(t, err)
	require.Empty(t, empty.Entries)
}

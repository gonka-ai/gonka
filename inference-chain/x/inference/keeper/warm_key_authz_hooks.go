package keeper

import (
	"context"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// WarmKeyAuthzHooks mirrors one authz write of the warm-key marker into the
// log. It is not installed: app.go's SetGrantHooks call is commented out
// because that method is not in github.com/gonka-ai/cosmos-sdk
// v0.53.3-ps19-observability. The post handler records transaction grants
// until the chain depends on a tag that has SetGrantHooks.
//
// When installed, it runs from SaveGrant and DeleteGrant, so it sees the
// grant being written and no other. It does not read the grant queue and
// does not look for grants that have already expired.
type WarmKeyAuthzHooks struct{ k *Keeper }

// WarmKeyAuthzHooks returns the hooks installed on the authz keeper.
func (k *Keeper) WarmKeyAuthzHooks() WarmKeyAuthzHooks { return WarmKeyAuthzHooks{k: k} }

// AfterGrantSaved appends a log entry for a marker grant. Any other message
// type is ignored.
func (h WarmKeyAuthzHooks) AfterGrantSaved(ctx context.Context, grantee, granter sdk.AccAddress, msgTypeURL string, expiration *time.Time) error {
	if !IsWarmKeyMarker(msgTypeURL) {
		return nil
	}
	_, err := h.k.RecordWarmKeyGrant(ctx, granter, grantee, expiration)
	return err
}

// AfterGrantDeleted ends the live log entry for a marker revoke. Expiry never
// reaches here: authz deletes an expired grant without calling DeleteGrant.
func (h WarmKeyAuthzHooks) AfterGrantDeleted(ctx context.Context, grantee, granter sdk.AccAddress, msgTypeURL string) error {
	if !IsWarmKeyMarker(msgTypeURL) {
		return nil
	}
	_, err := h.k.RecordWarmKeyRevoke(ctx, granter, grantee)
	return err
}

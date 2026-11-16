package app

import (
	"context"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// A different named interface reproduces the SDK API without importing a
// type that does not exist in the legacy SDK.
type registrationGrantHooks interface {
	AfterGrantSaved(context.Context, sdk.AccAddress, sdk.AccAddress, string, *time.Time) error
	AfterGrantDeleted(context.Context, sdk.AccAddress, sdk.AccAddress, string) error
}

type registrationKeeper struct{ hooks registrationGrantHooks }

func (k *registrationKeeper) SetGrantHooks(h registrationGrantHooks) { k.hooks = h }

type registrationHooks struct{ saved, deleted int }

func (h *registrationHooks) AfterGrantSaved(context.Context, sdk.AccAddress, sdk.AccAddress, string, *time.Time) error {
	h.saved++
	return nil
}

func (h *registrationHooks) AfterGrantDeleted(context.Context, sdk.AccAddress, sdk.AccAddress, string) error {
	h.deleted++
	return nil
}

type incompatibleRegistrationKeeper struct{}

func (*incompatibleRegistrationKeeper) SetGrantHooks(string) {}

func TestInstallWarmKeyGrantHooks(t *testing.T) {
	hooks := &registrationHooks{}
	require.False(t, installWarmKeyGrantHooks(&struct{}{}, hooks))
	k := &registrationKeeper{}
	require.True(t, installWarmKeyGrantHooks(k, hooks))
	require.NoError(t, k.hooks.AfterGrantSaved(context.Background(), nil, nil, "marker", nil))
	require.NoError(t, k.hooks.AfterGrantDeleted(context.Background(), nil, nil, "marker"))
	require.Equal(t, 1, hooks.saved)
	require.Equal(t, 1, hooks.deleted)
	require.PanicsWithValue(t, "authz: incompatible SetGrantHooks API; update warm-key hook registration", func() {
		installWarmKeyGrantHooks(&incompatibleRegistrationKeeper{}, hooks)
	})
}

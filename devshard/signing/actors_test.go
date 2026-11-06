package signing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Bind a warm key on slot 0 of a validator that also owns slot 1; slot 2 belongs to another validator.
//  2. Assert the rule accepts the cold key, the bound key, the key on the sibling slot while it is unbound and
//     refuses it there once slot 1 has its own binding; AcceptWarm runs only for a key the history does not cover.
func TestSlotActors_ColdWarmSiblingAccept(t *testing.T) {
	cold := "gonka1cold"
	other := "gonka1other"
	warm := "gonka1warm"
	stranger := "gonka1stranger"

	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, warm, 5, 1))
	a := SlotActors{
		SlotKeys: map[uint32]string{0: cold, 1: cold, 2: other},
		History:  h,
		Nonce:    10,
	}

	require.True(t, a.Allows(0, cold))
	require.True(t, a.Allows(0, warm))
	require.True(t, a.Allows(1, warm), "sibling slot of the same validator")
	require.False(t, a.Allows(2, warm), "warm bound for a different validator")
	require.False(t, a.Allows(0, stranger))
	require.False(t, a.Allows(9, cold))

	require.NoError(t, h.Bind(1, "gonka1otherwarm", 8, 2))
	require.False(t, a.Allows(1, warm), "a bound slot is exclusive; sibling must not override")
	require.True(t, a.Allows(1, "gonka1otherwarm"))
	require.True(t, a.Allows(1, cold))
	require.NoError(t, h.Revoke(1, "gonka1otherwarm", 9, 2))

	a.AcceptWarm = func(slotID uint32, recovered, expected string) bool {
		return slotID == 2 && recovered == stranger && expected == other
	}
	require.True(t, a.Allows(2, stranger))
	require.False(t, a.Allows(0, stranger), "AcceptWarm is asked with the slot and refuses the stranger there")
}

// Test flow:
//  1. Bind and revoke a key on slot 0, with an AcceptWarm that would accept it.
//  2. Assert the revoke wins over AcceptWarm at and after the revoke nonce, not before it.
func TestSlotActors_RevokeOverridesAcceptWarm(t *testing.T) {
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1warm", 5, 1))
	require.NoError(t, h.Revoke(0, "gonka1warm", 20, 1))
	a := SlotActors{
		SlotKeys:   map[uint32]string{0: "gonka1cold"},
		History:    h,
		AcceptWarm: func(uint32, string, string) bool { return true },
	}
	a.Nonce = 19
	require.True(t, a.Allows(0, "gonka1warm"))
	a.Nonce = 20
	require.False(t, a.Allows(0, "gonka1warm"))
	require.True(t, a.Allows(0, "gonka1other"), "a key the history never revoked still reaches AcceptWarm")
}

func TestExact_OnlyNamedKey(t *testing.T) {
	a := Exact(3, "gonka1only")
	require.True(t, a.Allows(3, "gonka1only"))
	require.False(t, a.Allows(3, "gonka1other"))
	require.False(t, a.Allows(0, "gonka1only"))
	require.False(t, Exact(1, "").Allows(1, "gonka1only"))
}

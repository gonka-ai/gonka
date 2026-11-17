package signing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlotActors_ColdWarmSiblingAccept(t *testing.T) {
	cold := "gonka1cold"
	other := "gonka1other"
	warm := "gonka1warm"
	stranger := "gonka1stranger"

	a := SlotActors{
		SlotKeys: map[uint32]string{0: cold, 1: cold, 2: other},
		WarmKeys: map[uint32]string{0: warm},
	}

	require.True(t, a.Allows(0, cold))
	require.True(t, a.Allows(0, warm))
	require.True(t, a.Allows(1, warm), "sibling slot of the same validator")
	require.False(t, a.Allows(2, warm), "warm bound for a different validator")
	require.False(t, a.Allows(0, stranger))
	require.False(t, a.Allows(9, cold))

	a.WarmKeys[1] = "gonka1otherwarm"
	require.False(t, a.Allows(1, warm), "a bound slot is exclusive; sibling must not override")
	require.True(t, a.Allows(1, "gonka1otherwarm"))
	require.True(t, a.Allows(1, cold))
	delete(a.WarmKeys, 1)

	a.AcceptWarm = func(slotID uint32, recovered, expected string) bool {
		return slotID == 2 && recovered == stranger && expected == other
	}
	require.True(t, a.Allows(2, stranger))
	require.False(t, a.Allows(0, stranger), "AcceptWarm must not run on a bound slot")
}

func TestExact_OnlyNamedKey(t *testing.T) {
	a := Exact(3, "gonka1only")
	require.True(t, a.Allows(3, "gonka1only"))
	require.False(t, a.Allows(3, "gonka1other"))
	require.False(t, a.Allows(0, "gonka1only"))
	require.False(t, Exact(1, "").Allows(1, "gonka1only"))
}

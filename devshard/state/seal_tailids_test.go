package state

import (
	"math/rand"
	"slices"
	"testing"

	"devshard/types"
)

func TestTailIDsMatchesFullSort(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for iter := 0; iter < 2000; iter++ {
		m := map[uint64]*types.InferenceRecord{}
		for i, n := 0, r.Intn(80); i < n; i++ {
			m[uint64(r.Intn(200))] = &types.InferenceRecord{}
		}
		w := 1 + r.Intn(20)
		var all []uint64
		for id := range m {
			all = append(all, id)
		}
		slices.Sort(all)
		if len(all) > w {
			all = all[len(all)-w:]
		}
		if got := tailIDs(m, w); !slices.Equal(got, all) && !(len(got) == 0 && len(all) == 0) {
			t.Fatalf("w=%d got %v want %v", w, got, all)
		}
	}
}

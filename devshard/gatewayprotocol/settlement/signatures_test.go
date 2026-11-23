package settlement

import (
	"cmp"
	"errors"
	"reflect"
	"testing"
)

type recordedSignatures struct {
	checked   map[uint32][]byte
	shortfall error
	held      map[uint32][]byte
}

func (r recordedSignatures) SettlementSignatures(uint64) (map[uint32][]byte, error) {
	return r.checked, r.shortfall
}

func (r recordedSignatures) SignaturesAt(uint64) map[uint32][]byte { return r.held }

// Test flow:
//  1. Ask for the quorum of groups of 0, 1, 3, 4 and 16 slots.
//  2. Assert each is more than two thirds of the slots, the chain's count: 1, 1, 3, 3 and 11.
func TestQuorumIsMoreThanTwoThirdsOfTheSlots(t *testing.T) {
	for slotCount, want := range map[int]int{0: 1, 1: 1, 3: 3, 4: 3, 16: 11} {
		if got := quorum(slotCount); got != want {
			t.Fatalf("quorum(%d) = %d, want %d", slotCount, got, want)
		}
	}
}

// Test flow:
//  1. Build sources for a 3-slot escrow: one whose re-check reaches quorum, one whose re-check falls short while it holds all 3 slots, one whose re-check falls short while it holds 2; and one for a 4-slot escrow whose re-check falls short while it holds 3.
//  2. Call `ChooseSignatures` on each.
//  3. Assert the checked set is posted with no shortfall, the held set is posted with the shortfall when it reaches the chain's quorum, and nothing is posted below it, with an error wrapping the shortfall.
func TestChooseSignaturesPostsTheHeldSetOnlyAtTheChainQuorum(t *testing.T) {
	shortfall := errors.New("insufficient valid signatures: 0/3 weight")
	held := map[uint32][]byte{0: []byte("held-0"), 1: []byte("held-1"), 2: []byte("held-2")}
	for _, testCase := range []struct {
		name        string
		slotCount   int
		source      recordedSignatures
		want        Signatures
		wantRefusal bool
	}{
		{name: "checked quorum", source: recordedSignatures{checked: map[uint32][]byte{0: []byte("checked-0")}, held: held},
			want: Signatures{BySlot: map[uint32][]byte{0: []byte("checked-0")}}},
		{name: "held quorum", source: recordedSignatures{shortfall: shortfall, held: held},
			want: Signatures{BySlot: held, Shortfall: shortfall}},
		{name: "held quorum of four slots", slotCount: 4, source: recordedSignatures{shortfall: shortfall, held: held},
			want: Signatures{BySlot: held, Shortfall: shortfall}},
		{name: "held short", source: recordedSignatures{shortfall: shortfall, held: map[uint32][]byte{0: []byte("held-0"), 2: []byte("held-2")}},
			wantRefusal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			chosen, err := ChooseSignatures(testCase.source, 9, cmp.Or(testCase.slotCount, 3))

			if testCase.wantRefusal {
				if !errors.Is(err, shortfall) || chosen.BySlot != nil {
					t.Fatalf("ChooseSignatures = %+v, %v; want nothing and an error wrapping %v", chosen, err, shortfall)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChooseSignatures error = %v, want nil", err)
			}
			if !reflect.DeepEqual(chosen, testCase.want) {
				t.Fatalf("ChooseSignatures = %+v, want %+v", chosen, testCase.want)
			}
		})
	}
}

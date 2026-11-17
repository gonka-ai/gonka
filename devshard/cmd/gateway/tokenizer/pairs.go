package tokenizer

import "math/bits"

const (
	pairHashMultiplier  = 0x9E3779B97F4A7C15
	pairLoadNumerator   = 3
	pairLoadDenominator = 4
	noRank              = ^uint32(0)
	filterBitsPerEntry  = 16
	filterWordBits      = 64
)

// pairTable maps a token pair to its merge, open-addressed over a power-of-two slice kept at most three quarters full.
type pairTable struct {
	slots       []pairSlot
	shift       uint
	filter      []uint64
	filterShift uint
}

type pairSlot struct {
	key   uint64
	merge merge
}

func newPairTable(entries int) *pairTable {
	size := 1 << bits.Len(uint(entries*pairLoadDenominator/pairLoadNumerator))
	table := &pairTable{slots: make([]pairSlot, size), shift: uint(bits.UintSize - bits.Len(uint(size-1)))}
	for index := range table.slots {
		table.slots[index].merge.rank = noRank
	}
	filterBits := 1 << bits.Len(uint(entries*filterBitsPerEntry))
	table.filter = make([]uint64, max(1, filterBits/filterWordBits))
	table.filterShift = uint(bits.UintSize - bits.Len(uint(filterBits-1)))
	return table
}

func (t *pairTable) index(key uint64) uint { return uint(key * pairHashMultiplier >> t.shift) }

func (t *pairTable) add(key uint64, found merge) {
	bit := key * pairHashMultiplier >> t.filterShift
	t.filter[bit/filterWordBits] |= 1 << (bit % filterWordBits)
	mask := uint(len(t.slots) - 1)
	for at := t.index(key); ; at = (at + 1) & mask {
		slot := &t.slots[at]
		if slot.merge.rank == noRank {
			*slot = pairSlot{key: key, merge: found}
			return
		}
		if slot.key == key {
			return
		}
	}
}

func (t *pairTable) lookup(left, right uint32) (merge, bool) {
	key := pairKey(left, right)
	bit := key * pairHashMultiplier >> t.filterShift
	if t.filter[bit/filterWordBits]&(1<<(bit%filterWordBits)) == 0 {
		return merge{}, false
	}
	mask := uint(len(t.slots) - 1)
	for at := t.index(key); ; at = (at + 1) & mask {
		slot := &t.slots[at]
		if slot.key == key && slot.merge.rank != noRank {
			return slot.merge, true
		}
		if slot.merge.rank == noRank {
			return merge{}, false
		}
	}
}

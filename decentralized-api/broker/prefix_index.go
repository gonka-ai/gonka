package broker

import "container/list"

// prefixIndex is a bounded LRU of the prompt block hashes the broker routed to one node. It has no
// lock of its own: the broker reads it under b.mu.RLock and writes it under b.mu.Lock.
type prefixIndex struct {
	capacity int
	order    *list.List
	entries  map[uint64]*list.Element
}

func newPrefixIndex(capacity int) *prefixIndex {
	return &prefixIndex{capacity: capacity, order: list.New(), entries: make(map[uint64]*list.Element)}
}

// matchedPrefix counts the leading hashes present in the index, stopping at the first miss. It does not touch recency.
func (p *prefixIndex) matchedPrefix(hashes []uint64) int {
	if p == nil {
		return 0
	}
	n := 0
	for n < len(hashes) {
		if _, ok := p.entries[hashes[n]]; !ok {
			break
		}
		n++
	}
	return n
}

// add marks every hash most recently used and evicts the least recently used ones above capacity.
// Hashes go in back to front so the chain's root is the newest and eviction shortens it from the tail.
func (p *prefixIndex) add(hashes []uint64) {
	for i := len(hashes) - 1; i >= 0; i-- {
		h := hashes[i]
		if el, ok := p.entries[h]; ok {
			p.order.MoveToFront(el)
			continue
		}
		p.entries[h] = p.order.PushFront(h)
	}
	for p.order.Len() > p.capacity {
		oldest := p.order.Back()
		delete(p.entries, oldest.Value.(uint64))
		p.order.Remove(oldest)
	}
}

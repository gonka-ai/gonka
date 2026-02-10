package keeper

import (
	"sync"

	"github.com/tidwall/btree"
)

const (
	// NormalizedParticipantsCacheBlocks is how many blocks to keep in the normalized weighted participants cache.
	// Entries older than (currentHeight - NormalizedParticipantsCacheBlocks) are evicted in BeginBlock.
	NormalizedParticipantsCacheBlocks = 300
	// NormalizedParticipantsSampleSize is how many participants we pick per inference from the normalized tree.
	NormalizedParticipantsSampleSize = 10
	// NormalizedParticipantsMaxSampleIterations caps the sampling loop (avoid long spins when few unique participants).
	NormalizedParticipantsMaxSampleIterations = NormalizedParticipantsSampleSize * 3 / 2 // 10*1.5 = 15
)

// blockRef is a (blockHeight, blockHash) tuple for the FIFO eviction queue.
type blockRef struct {
	Height int64
	Hash   []byte
}

// ParticipantWeight is address and weight for building the normalized cache.
type ParticipantWeight struct {
	Address string
	Weight  int64
}

// normalizedWeightedParticipantsCache is an in-memory cache: blockHash -> BTree(cumulative normalized weight -> participant address).
// A FIFO queue of (blockHeight, blockHash) drives eviction by height.
type normalizedWeightedParticipantsCache struct {
	mu     sync.RWMutex
	byHash map[string]*btree.Map[float64, string] // key = string(blockHash)
	queue  []blockRef
}

func newNormalizedWeightedParticipantsCache() *normalizedWeightedParticipantsCache {
	return &normalizedWeightedParticipantsCache{
		byHash: make(map[string]*btree.Map[float64, string]),
		queue:  nil,
	}
}

// BuildNormalizedTree returns a BTree of cumulative normalized weight -> participant address.
// Participants with weight <= 0 are dropped. Keys are cumulative normalized weights (weight/totalWeight).
func (c *normalizedWeightedParticipantsCache) BuildNormalizedTree(participants []ParticipantWeight) *btree.Map[float64, string] {
	var total int64
	for _, p := range participants {
		total += p.Weight
	}
	tree := btree.NewMap[float64, string](32)
	if total <= 0 {
		return tree
	}
	var cum float64
	for _, p := range participants {
		norm := float64(p.Weight) / float64(total)
		cum += norm
		tree.Set(cum, p.Address)
	}
	return tree
}

// Add builds a normalized BTree from participant weights and stores it by blockHash.
// (blockHeight, blockHash) is appended to the FIFO queue. Weights are normalized (weight/totalWeight),
// participants with weight <= 0 are dropped, and the BTree keys are cumulative normalized weights.
func (c *normalizedWeightedParticipantsCache) Add(blockHash []byte, blockHeight int64, participants []ParticipantWeight) {
	if len(blockHash) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tree := c.BuildNormalizedTree(participants)
	key := string(blockHash)
	c.byHash[key] = tree
	c.queue = append(c.queue, blockRef{Height: blockHeight, Hash: append([]byte(nil), blockHash...)})
}

// ClearByHeight removes all entries with height < targetHeight from the cache (FIFO drain and delete by hash).
func (c *normalizedWeightedParticipantsCache) ClearByHeight(targetHeight int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := 0
	for i < len(c.queue) && c.queue[i].Height < targetHeight {
		ref := c.queue[i]
		delete(c.byHash, string(ref.Hash))
		i++
	}
	if i > 0 {
		c.queue = c.queue[i:]
	}
}

// Get returns the BTree for the given blockHash, or (nil, false) if not present.
// The tree maps cumulative normalized weight (float64) to participant address (string) for weighted sampling.
func (c *normalizedWeightedParticipantsCache) Get(blockHash []byte) (*btree.Map[float64, string], bool) {
	if len(blockHash) == 0 {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tree, ok := c.byHash[string(blockHash)]
	return tree, ok
}

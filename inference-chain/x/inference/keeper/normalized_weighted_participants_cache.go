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

// blockRef is a (blockHeight, blockHash, modelId) tuple for the FIFO eviction queue.
type blockRef struct {
	Height int64
	Hash   []byte
	Model  string
}

// ParticipantWeight is address and weight for building the normalized cache.
type ParticipantWeight struct {
	Address string
	Weight  int64
}

// normalizedWeightedParticipantsCache is an in-memory cache:
//
//	(blockHash, modelId) -> BTree(cumulative normalized weight -> participant address).
//
// A FIFO queue of (blockHeight, blockHash, modelId) drives eviction by height.
type normalizedWeightedParticipantsCache struct {
	mu    sync.RWMutex
	byKey map[string]*btree.Map[float64, string] // key = string(blockHash) + "|" + modelId
	queue []blockRef
}

func newNormalizedWeightedParticipantsCache() *normalizedWeightedParticipantsCache {
	return &normalizedWeightedParticipantsCache{
		byKey: make(map[string]*btree.Map[float64, string]),
		queue: nil,
	}
}

// makeNormKey builds the cache key for (blockHash, modelId).
func makeNormKey(blockHash []byte, modelId string) string {
	return string(blockHash) + "|" + modelId
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

// Add builds a normalized BTree from participant weights and stores it by (blockHash, modelId).
// (blockHeight, blockHash, modelId) is appended to the FIFO queue.
func (c *normalizedWeightedParticipantsCache) Add(blockHash []byte, blockHeight int64, modelId string, participants []ParticipantWeight) {
	if len(blockHash) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tree := c.BuildNormalizedTree(participants)
	key := makeNormKey(blockHash, modelId)
	c.byKey[key] = tree
	c.queue = append(c.queue, blockRef{
		Height: blockHeight,
		Hash:   append([]byte(nil), blockHash...),
		Model:  modelId,
	})
}

// ClearByHeight removes all entries with height < targetHeight from the cache (FIFO drain and delete by hash).
func (c *normalizedWeightedParticipantsCache) ClearByHeight(targetHeight int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := 0
	for i < len(c.queue) && c.queue[i].Height < targetHeight {
		ref := c.queue[i]
		delete(c.byKey, makeNormKey(ref.Hash, ref.Model))
		i++
	}
	if i > 0 {
		c.queue = c.queue[i:]
	}
}

// Get returns the BTree for the given (blockHash, modelId), or (nil, false) if not present.
// The tree maps cumulative normalized weight (float64) to participant address (string) for weighted sampling.
func (c *normalizedWeightedParticipantsCache) Get(blockHash []byte, modelId string) (*btree.Map[float64, string], bool) {
	if len(blockHash) == 0 {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	tree, ok := c.byKey[makeNormKey(blockHash, modelId)]
	return tree, ok
}

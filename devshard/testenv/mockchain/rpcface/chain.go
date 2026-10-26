package rpcface

import (
	"fmt"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"devshard/testenv/mockchain/store"
)

const (
	// retainedBlocks bounds the blocks kept in memory. Lower heights answer
	// "not available", like a pruned node. Pruning runs once per pruneChunk
	// new blocks.
	retainedBlocks = 20_000
	pruneChunk     = 1_000
	validatorPower = 10
)

// mockBlock is one fabricated block and the commit that signs it.
type mockBlock struct {
	block  *cmttypes.Block
	id     cmttypes.BlockID
	commit *cmttypes.Commit
}

// headerChain fabricates hash-linked blocks signed by one fixed ed25519
// validator, so a commit-verifying light client accepts mock-chain headers
// the way it accepts a real node's. The block, header, commit and blockchain
// routes and the NewBlock event all read the same block for a height.
//
// A block gets its time when it is first built: the ticker builds each new
// height as it is produced, so that is the production time. Heights that
// already existed at the first lookup are dated back from it one block
// interval apart.
type headerChain struct {
	store    *store.Store
	interval time.Duration
	now      func() time.Time
	key      ed25519.PrivKey
	vals     *cmttypes.ValidatorSet

	mu     sync.Mutex
	base   int64       // height of blocks[0]
	blocks []mockBlock // contiguous from base
}

func newHeaderChain(st *store.Store, interval time.Duration) *headerChain {
	key := ed25519.GenPrivKeyFromSecret([]byte("mock-chain validator"))
	vals := cmttypes.NewValidatorSet([]*cmttypes.Validator{cmttypes.NewValidator(key.PubKey(), validatorPower)})
	return &headerChain{
		store:    st,
		interval: max(interval, time.Millisecond),
		now:      time.Now,
		key:      key,
		vals:     vals,
	}
}

func (c *headerChain) validators() *cmttypes.ValidatorSet {
	return c.vals.Copy()
}

// lowest returns the lowest height still served, or 0 before the first lookup.
func (c *headerChain) lowest() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base
}

// at returns the block at height, building it and any missing height below it.
func (c *headerChain) at(height int64) (mockBlock, error) {
	if height <= 0 {
		return mockBlock{}, fmt.Errorf("height must be greater than 0, but got %d", height)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.blocks) == 0 {
		if err := c.initLocked(max(height, c.store.GetBlockHeight())); err != nil {
			return mockBlock{}, err
		}
	}
	for top := c.base + int64(len(c.blocks)) - 1; top < height; top++ {
		prev := &c.blocks[len(c.blocks)-1]
		t := c.now().UTC().Truncate(time.Millisecond)
		if floor := prev.block.Time.Add(time.Millisecond); t.Before(floor) {
			t = floor
		}
		mb, err := c.build(top+1, t, prev)
		if err != nil {
			return mockBlock{}, err
		}
		c.blocks = append(c.blocks, mb)
	}
	if drop := len(c.blocks) - retainedBlocks; drop >= pruneChunk {
		c.blocks = append([]mockBlock(nil), c.blocks[drop:]...)
		c.base += int64(drop)
	}
	if height < c.base {
		return mockBlock{}, fmt.Errorf("height %d is not available, lowest height is %d", height, c.base)
	}
	return c.blocks[height-c.base], nil
}

func (c *headerChain) initLocked(tip int64) error {
	start := max(1, tip-retainedBlocks+1)
	tipTime := c.now().UTC().Truncate(time.Millisecond)
	blocks := make([]mockBlock, 0, tip-start+1)
	var prev *mockBlock
	for h := start; h <= tip; h++ {
		mb, err := c.build(h, tipTime.Add(-time.Duration(tip-h)*c.interval), prev)
		if err != nil {
			return err
		}
		blocks = append(blocks, mb)
		prev = &blocks[len(blocks)-1]
	}
	c.base, c.blocks = start, blocks
	return nil
}

func (c *headerChain) build(height int64, t time.Time, prev *mockBlock) (mockBlock, error) {
	chainID := c.store.GetChainID()
	lastCommit := &cmttypes.Commit{}
	var lastID cmttypes.BlockID
	if prev != nil {
		lastCommit, lastID = prev.commit, prev.id
	}
	block := cmttypes.MakeBlock(height, nil, lastCommit, nil)
	block.ChainID = chainID
	block.Time = t
	block.LastBlockID = lastID
	block.ValidatorsHash = c.vals.Hash()
	block.NextValidatorsHash = c.vals.Hash()
	block.ProposerAddress = c.vals.Validators[0].Address

	hash := block.Hash()
	parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
	if err != nil {
		return mockBlock{}, fmt.Errorf("mockchain rpc: block %d parts: %w", height, err)
	}
	id := cmttypes.BlockID{Hash: hash, PartSetHeader: parts.Header()}

	val := c.vals.Validators[0]
	vote := &cmttypes.Vote{
		Type:             cmtproto.PrecommitType,
		Height:           height,
		BlockID:          id,
		Timestamp:        t,
		ValidatorAddress: val.Address,
	}
	sig, err := c.key.Sign(cmttypes.VoteSignBytes(chainID, vote.ToProto()))
	if err != nil {
		return mockBlock{}, fmt.Errorf("mockchain rpc: sign block %d: %w", height, err)
	}
	vote.Signature = sig
	commit := &cmttypes.Commit{Height: height, BlockID: id, Signatures: []cmttypes.CommitSig{vote.CommitSig()}}
	return mockBlock{block: block, id: id, commit: commit}, nil
}

package rpcface

import (
	"fmt"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"

	"devshard/testenv/mockchain/store"
)

const (
	validatorPower = 10
	// blockchainInfoLimit is CometBFT's cap on one blockchain response.
	blockchainInfoLimit = 20
)

// chainHeaders fabricates the chain's headers and signs their commits with one fixed validator, so a light client that checks signatures accepts what the RPC serves.
// A header takes its time when it is first built and keeps it: heights up to the store's starting one lie behind it at the block interval, later ones are stamped now but at least 1µs after the previous header, so a burst of N blocks leaves the tip at most about N µs ahead of the clock.
type chainHeaders struct {
	st       *store.Store
	key      ed25519.PrivKey
	vals     *cmttypes.ValidatorSet
	chainID  string
	interval time.Duration

	mu          sync.Mutex
	headers     []*cmttypes.Header // headers[h-1] is the header at height h
	startHeight int64
	startTime   time.Time
}

func newChainHeaders(st *store.Store, interval time.Duration) *chainHeaders {
	chainID := st.GetChainID()
	key := ed25519.GenPrivKeyFromSecret([]byte("mock-chain-validator:" + chainID))
	return &chainHeaders{
		st:          st,
		key:         key,
		vals:        cmttypes.NewValidatorSet([]*cmttypes.Validator{cmttypes.NewValidator(key.PubKey(), validatorPower)}),
		chainID:     chainID,
		interval:    interval,
		startHeight: st.GetBlockHeight(),
		startTime:   time.Now().UTC(),
	}
}

func blockIDOf(header *cmttypes.Header) cmttypes.BlockID {
	hash := header.Hash()
	return cmttypes.BlockID{Hash: hash, PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: tmhash.Sum(hash)}}
}

func (c *chainHeaders) headerAt(height int64) *cmttypes.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	for int64(len(c.headers)) < height {
		c.headers = append(c.headers, c.build(int64(len(c.headers))+1))
	}
	return c.headers[height-1]
}

// build makes the header at height from its predecessor; the caller holds c.mu.
func (c *chainHeaders) build(height int64) *cmttypes.Header {
	var lastBlockID cmttypes.BlockID
	at := c.startTime.Add(-time.Duration(c.startHeight-height) * c.interval)
	if height > c.startHeight {
		at = time.Now().UTC()
	}
	if height > 1 {
		previous := c.headers[height-2]
		lastBlockID = blockIDOf(previous)
		if height > c.startHeight {
			at = maxTime(at, previous.Time.Add(time.Microsecond))
		}
	}
	valsHash := c.vals.Hash()
	return &cmttypes.Header{
		Version:            cmtversion.Consensus{Block: version.BlockProtocol},
		ChainID:            c.chainID,
		Height:             height,
		Time:               at,
		LastBlockID:        lastBlockID,
		ValidatorsHash:     valsHash,
		NextValidatorsHash: valsHash,
		ConsensusHash:      tmhash.Sum([]byte("mock-chain-consensus")),
		ProposerAddress:    c.vals.Validators[0].Address,
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (c *chainHeaders) commitAt(height int64) *cmttypes.Commit {
	header := c.headerAt(height)
	commit := &cmttypes.Commit{
		Height:  height,
		BlockID: blockIDOf(header),
		Signatures: []cmttypes.CommitSig{{
			BlockIDFlag:      cmttypes.BlockIDFlagCommit,
			ValidatorAddress: c.vals.Validators[0].Address,
			Timestamp:        header.Time,
		}},
	}
	signature, err := c.key.Sign(commit.VoteSignBytes(c.chainID, 0))
	if err != nil {
		panic(fmt.Sprintf("mockchain rpc: sign commit at %d: %v", height, err))
	}
	commit.Signatures[0].Signature = signature
	return commit
}

// BlockAt is the header and block id the RPC serves at height; a nil height asks for the latest.
func (s *Service) BlockAt(height *int64) (*cmttypes.Header, cmttypes.BlockID, error) {
	resolved, err := s.chain.resolve(height)
	if err != nil {
		return nil, cmttypes.BlockID{}, err
	}
	header := s.chain.headerAt(resolved)
	return header, blockIDOf(header), nil
}

// resolve turns an optional height into a served one, with CometBFT's wording for the refusals.
func (c *chainHeaders) resolve(heightPtr *int64) (int64, error) {
	latest := c.st.GetBlockHeight()
	height := latest
	if heightPtr != nil {
		height = *heightPtr
	}
	if height <= 0 {
		return 0, fmt.Errorf("height must be greater than 0, but got %d", height)
	}
	if height > latest {
		return 0, fmt.Errorf("height %d must be less than or equal to the current blockchain height %d", height, latest)
	}
	return height, nil
}

func (c *chainHeaders) commitHandler(_ *rpctypes.Context, heightPtr *int64) (*ctypes.ResultCommit, error) {
	height, err := c.resolve(heightPtr)
	if err != nil {
		return nil, err
	}
	return ctypes.NewResultCommit(c.headerAt(height), c.commitAt(height), true), nil
}

func (c *chainHeaders) headerHandler(_ *rpctypes.Context, heightPtr *int64) (*ctypes.ResultHeader, error) {
	height, err := c.resolve(heightPtr)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultHeader{Header: c.headerAt(height)}, nil
}

func (c *chainHeaders) validatorsHandler(_ *rpctypes.Context, heightPtr *int64, _, _ *int) (*ctypes.ResultValidators, error) {
	height, err := c.resolve(heightPtr)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultValidators{
		BlockHeight: height,
		Validators:  c.vals.Validators,
		Count:       len(c.vals.Validators),
		Total:       len(c.vals.Validators),
	}, nil
}

func (c *chainHeaders) blockchainHandler(_ *rpctypes.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	latest := c.st.GetBlockHeight()
	if minHeight <= 0 {
		minHeight = 1
	}
	if maxHeight <= 0 || maxHeight > latest {
		maxHeight = latest
	}
	if minHeight > maxHeight {
		return nil, fmt.Errorf("min height %d can't be greater than max height %d", minHeight, maxHeight)
	}
	minHeight = max(minHeight, maxHeight-blockchainInfoLimit+1)
	metas := make([]*cmttypes.BlockMeta, 0, maxHeight-minHeight+1)
	for height := maxHeight; height >= minHeight; height-- {
		header := c.headerAt(height)
		metas = append(metas, &cmttypes.BlockMeta{BlockID: blockIDOf(header), Header: *header})
	}
	return &ctypes.ResultBlockchainInfo{LastHeight: latest, BlockMetas: metas}, nil
}

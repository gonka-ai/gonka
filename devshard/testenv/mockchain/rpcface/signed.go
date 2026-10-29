package rpcface

import (
	"fmt"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

// signedBlocks is one testenv validator and the headers it has signed.
// NewBlock, /header, /commit, /validators, and BlockchainInfo share these.
type signedBlocks struct {
	mu      sync.Mutex
	vals    *cmttypes.ValidatorSet
	priv    cmttypes.PrivValidator
	chainID string
	headers map[int64]*cmttypes.SignedHeader
	tip     int64
}

func newSignedBlocks(chainID string) (*signedBlocks, error) {
	vals, privs := cmttypes.RandValidatorSet(1, 10)
	if vals == nil || len(privs) != 1 {
		return nil, fmt.Errorf("mockchain rpc: single-validator set")
	}
	return &signedBlocks{
		vals:    vals,
		priv:    privs[0],
		chainID: chainID,
		headers: make(map[int64]*cmttypes.SignedHeader),
	}, nil
}

func (c *signedBlocks) ensureThrough(tip int64) error {
	if c == nil || tip <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for h := c.tip + 1; h <= tip; h++ {
		var last cmttypes.BlockID
		if prev, ok := c.headers[h-1]; ok {
			last = prev.Commit.BlockID
		}
		sh, err := c.signLocked(h, last)
		if err != nil {
			return err
		}
		c.headers[h] = sh
	}
	if tip > c.tip {
		c.tip = tip
	}
	return nil
}

func (c *signedBlocks) signLocked(height int64, last cmttypes.BlockID) (*cmttypes.SignedHeader, error) {
	valsHash := c.vals.Hash()
	hdr := &cmttypes.Header{
		Version:            cmtversion.Consensus{Block: version.BlockProtocol},
		ChainID:            c.chainID,
		Height:             height,
		Time:               time.Now().UTC().Truncate(time.Millisecond),
		LastBlockID:        last,
		LastCommitHash:     tmhash.Sum([]byte("last-commit")),
		DataHash:           tmhash.Sum([]byte("data")),
		ValidatorsHash:     valsHash,
		NextValidatorsHash: valsHash,
		ConsensusHash:      tmhash.Sum([]byte("consensus")),
		AppHash:            tmhash.Sum([]byte("app")),
		LastResultsHash:    tmhash.Sum([]byte("results")),
		EvidenceHash:       tmhash.Sum([]byte("evidence")),
		ProposerAddress:    c.vals.Validators[0].Address,
	}
	blockID := cmttypes.BlockID{
		Hash:          hdr.Hash(),
		PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: tmhash.Sum([]byte("parts"))},
	}
	vote := &cmttypes.Vote{
		Type:             cmtproto.PrecommitType,
		Height:           height,
		Round:            0,
		BlockID:          blockID,
		Timestamp:        hdr.Time,
		ValidatorAddress: c.vals.Validators[0].Address,
		ValidatorIndex:   0,
	}
	pb := vote.ToProto()
	if err := c.priv.SignVote(c.chainID, pb); err != nil {
		return nil, fmt.Errorf("mockchain rpc: sign height %d: %w", height, err)
	}
	vote.Signature = pb.Signature
	return &cmttypes.SignedHeader{
		Header: hdr,
		Commit: &cmttypes.Commit{
			Height:     height,
			Round:      0,
			BlockID:    blockID,
			Signatures: []cmttypes.CommitSig{vote.CommitSig()},
		},
	}, nil
}

func (c *signedBlocks) at(height int64) (*cmttypes.SignedHeader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sh, ok := c.headers[height]
	if !ok {
		return nil, fmt.Errorf("height %d must be less than or equal to the current blockchain height", height)
	}
	hdr := *sh.Header
	commit := *sh.Commit
	return &cmttypes.SignedHeader{Header: &hdr, Commit: &commit}, nil
}

func (c *signedBlocks) resultBlock(height int64) (*ctypes.ResultBlock, error) {
	sh, err := c.at(height)
	if err != nil {
		return nil, err
	}
	var lastCommit *cmttypes.Commit
	if height > 1 {
		prev, err := c.at(height - 1)
		if err != nil {
			return nil, err
		}
		lastCommit = prev.Commit
	}
	block := &cmttypes.Block{
		Header:     *sh.Header,
		Data:       cmttypes.Data{},
		Evidence:   cmttypes.EvidenceData{},
		LastCommit: lastCommit,
	}
	return &ctypes.ResultBlock{
		BlockID: sh.Commit.BlockID,
		Block:   block,
	}, nil
}

func (c *signedBlocks) newBlockEvent(height int64) (cmttypes.EventDataNewBlock, error) {
	rb, err := c.resultBlock(height)
	if err != nil {
		return cmttypes.EventDataNewBlock{}, err
	}
	return cmttypes.EventDataNewBlock{
		Block:   rb.Block,
		BlockID: rb.BlockID,
	}, nil
}

func (c *signedBlocks) validatorSet() *cmttypes.ValidatorSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vals.Copy()
}

func (s *Service) resolveHeight(heightPtr *int64) (int64, error) {
	latest := s.store.GetBlockHeight()
	height := latest
	if heightPtr != nil {
		height = *heightPtr
	}
	if height <= 0 {
		return 0, fmt.Errorf("height must be greater than 0, but got %d", height)
	}
	if height > latest {
		return 0, fmt.Errorf("height %d must be less than or equal to the current blockchain height %d",
			height, latest)
	}
	if err := s.signed.ensureThrough(height); err != nil {
		return 0, err
	}
	return height, nil
}

func (s *Service) blockRPC(_ *rpctypes.Context, heightPtr *int64) (*ctypes.ResultBlock, error) {
	height, err := s.resolveHeight(heightPtr)
	if err != nil {
		return nil, err
	}
	return s.signed.resultBlock(height)
}

func (s *Service) headerRPC(_ *rpctypes.Context, heightPtr *int64) (*ctypes.ResultHeader, error) {
	height, err := s.resolveHeight(heightPtr)
	if err != nil {
		return nil, err
	}
	sh, err := s.signed.at(height)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultHeader{Header: sh.Header}, nil
}

func (s *Service) commitRPC(_ *rpctypes.Context, heightPtr *int64) (*ctypes.ResultCommit, error) {
	height, err := s.resolveHeight(heightPtr)
	if err != nil {
		return nil, err
	}
	sh, err := s.signed.at(height)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultCommit{SignedHeader: *sh, CanonicalCommit: true}, nil
}

func (s *Service) validatorsRPC(_ *rpctypes.Context, heightPtr *int64, _, _ *int) (*ctypes.ResultValidators, error) {
	height, err := s.resolveHeight(heightPtr)
	if err != nil {
		return nil, err
	}
	vals := s.signed.validatorSet()
	return &ctypes.ResultValidators{
		BlockHeight: height,
		Validators:  vals.Validators,
		Count:       vals.Size(),
		Total:       vals.Size(),
	}, nil
}

func (s *Service) blockchainRPC(_ *rpctypes.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	latest := s.store.GetBlockHeight()
	if maxHeight <= 0 || maxHeight > latest {
		maxHeight = latest
	}
	if minHeight <= 0 {
		minHeight = 1
	}
	if minHeight > maxHeight {
		return nil, fmt.Errorf("min height can't be greater than max height")
	}
	if err := s.signed.ensureThrough(maxHeight); err != nil {
		return nil, err
	}
	res := &ctypes.ResultBlockchainInfo{LastHeight: latest}
	for h := maxHeight; h >= minHeight; h-- {
		sh, err := s.signed.at(h)
		if err != nil {
			return nil, err
		}
		res.BlockMetas = append(res.BlockMetas, &cmttypes.BlockMeta{
			BlockID: sh.Commit.BlockID,
			Header:  *sh.Header,
		})
	}
	return res, nil
}

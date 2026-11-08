package observer

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"common/chainoracle/blocks"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

const SignedChainID = "gonka-verify"

// SignedChain is an in-process signed Comet chain. Tests use it as the RPC
// behind NewTendermint (through ServeJSONRPC) and as a source of dapi BLOCK
// pages.
type SignedChain struct {
	vals    *cmttypes.ValidatorSet
	privs   []cmttypes.PrivValidator
	base    time.Time
	mu      sync.Mutex
	headers map[int64]*cmttypes.SignedHeader
	tip     int64

	tamperTime   map[int64]bool
	forgeCommits map[int64]bool
	commitCalls  atomic.Int64
	batchCalls   atomic.Int64
	batchRanges  [][2]int64

	commitHold   chan struct{}
	batchHold    chan struct{}
	batchStarted chan struct{}
	batchOnce    sync.Once
	commitMax    int64
}

type signedChain = SignedChain

func (c *SignedChain) blockCommit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commitHold = make(chan struct{})
}

func (c *SignedChain) releaseCommit() {
	c.mu.Lock()
	ch := c.commitHold
	c.commitHold = nil
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (c *SignedChain) blockBatch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchHold = make(chan struct{})
	c.batchStarted = make(chan struct{})
	c.batchOnce = sync.Once{}
}

func (c *SignedChain) releaseBatch() {
	c.mu.Lock()
	ch := c.batchHold
	c.batchHold = nil
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// NewSignedChain builds a 4-validator chain whose tip is `tip`.
func NewSignedChain(tip int64) *SignedChain {
	vals, privs := cmttypes.RandValidatorSet(4, 10)
	c := &SignedChain{
		vals:         vals,
		privs:        privs,
		base:         time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond),
		headers:      make(map[int64]*cmttypes.SignedHeader),
		tamperTime:   make(map[int64]bool),
		forgeCommits: make(map[int64]bool),
	}
	c.grow(tip)
	return c
}

func (c *SignedChain) blockTime(height int64) time.Time {
	return c.base.Add(time.Duration(height) * 5 * time.Second)
}

func (c *SignedChain) grow(tip int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h := c.tip + 1; h <= tip; h++ {
		var last cmttypes.BlockID
		if prev, ok := c.headers[h-1]; ok {
			last = prev.Commit.BlockID
		}
		c.headers[h] = c.sign(h, last, c.vals, c.privs)
	}
	if tip > c.tip {
		c.tip = tip
	}
}

// Grow extends the chain to tip, signing each new height.
func (c *SignedChain) Grow(tip int64) { c.grow(tip) }

func (c *SignedChain) sign(height int64, last cmttypes.BlockID, vals *cmttypes.ValidatorSet, privs []cmttypes.PrivValidator) *cmttypes.SignedHeader {
	return c.signWithNext(height, last, vals, privs, vals)
}

func (c *SignedChain) signWithNext(height int64, last cmttypes.BlockID, vals *cmttypes.ValidatorSet, privs []cmttypes.PrivValidator, next *cmttypes.ValidatorSet) *cmttypes.SignedHeader {
	hdr := &cmttypes.Header{
		Version:            cmtversion.Consensus{Block: version.BlockProtocol},
		ChainID:            SignedChainID,
		Height:             height,
		Time:               c.blockTime(height),
		LastBlockID:        last,
		LastCommitHash:     tmhash.Sum([]byte("last-commit")),
		DataHash:           tmhash.Sum([]byte("data")),
		ValidatorsHash:     vals.Hash(),
		NextValidatorsHash: next.Hash(),
		ConsensusHash:      tmhash.Sum([]byte("consensus")),
		AppHash:            tmhash.Sum([]byte("app")),
		LastResultsHash:    tmhash.Sum([]byte("results")),
		EvidenceHash:       tmhash.Sum([]byte("evidence")),
		ProposerAddress:    vals.Validators[0].Address,
	}
	blockID := cmttypes.BlockID{Hash: hdr.Hash(), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: tmhash.Sum([]byte("parts"))}}
	sigs := make([]cmttypes.CommitSig, len(vals.Validators))
	for i, v := range vals.Validators {
		vote := &cmttypes.Vote{
			Type: cmtproto.PrecommitType, Height: height, Round: 0, BlockID: blockID,
			Timestamp: hdr.Time, ValidatorAddress: v.Address, ValidatorIndex: int32(i),
		}
		pb := vote.ToProto()
		var signer cmttypes.PrivValidator
		for _, pv := range privs {
			pk, err := pv.GetPubKey()
			if err != nil {
				panic(err)
			}
			if bytes.Equal(pk.Address(), v.Address) {
				signer = pv
			}
		}
		if signer == nil {
			panic("signed chain: missing validator signer")
		}
		if err := signer.SignVote(SignedChainID, pb); err != nil {
			panic(err)
		}
		vote.Signature = pb.Signature
		sigs[i] = vote.CommitSig()
	}
	return &cmttypes.SignedHeader{Header: hdr, Commit: &cmttypes.Commit{Height: height, Round: 0, BlockID: blockID, Signatures: sigs}}
}

func (c *SignedChain) Header(_ context.Context, height *int64) (*ctypes.ResultHeader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.tip
	if height != nil {
		h = *height
	}
	sh, ok := c.headers[h]
	if !ok {
		return nil, errors.New("height must be less than or equal to the current blockchain height")
	}
	return &ctypes.ResultHeader{Header: sh.Header}, nil
}

func (c *SignedChain) capCommit(tip int64) {
	c.mu.Lock()
	c.commitMax = tip
	c.mu.Unlock()
}

func (c *SignedChain) Commit(_ context.Context, height *int64) (*ctypes.ResultCommit, error) {
	c.commitCalls.Add(1)
	c.mu.Lock()
	hold := c.commitHold
	c.mu.Unlock()
	if hold != nil {
		<-hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.tip
	if height != nil {
		h = *height
	}
	if c.commitMax > 0 && h > c.commitMax {
		return nil, errors.New("height must be less than or equal to the current blockchain height")
	}
	sh, ok := c.headers[h]
	if !ok {
		return nil, errors.New("height must be less than or equal to the current blockchain height")
	}
	if c.forgeCommits[h] {
		forgedVals, forgedPrivs := cmttypes.RandValidatorSet(4, 10)
		forged := c.sign(h, sh.LastBlockID, forgedVals, forgedPrivs)
		return &ctypes.ResultCommit{SignedHeader: *forged}, nil
	}
	return &ctypes.ResultCommit{SignedHeader: *sh, CanonicalCommit: true}, nil
}

func (c *SignedChain) Validators(_ context.Context, height *int64, page, perPage *int) (*ctypes.ResultValidators, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if height != nil && c.forgeCommits[*height] {
		return nil, errors.New("validators withheld")
	}
	h := c.tip
	if height != nil {
		h = *height
	}
	return &ctypes.ResultValidators{BlockHeight: h, Validators: c.vals.Copy().Validators, Count: c.vals.Size(), Total: c.vals.Size()}, nil
}

func (c *SignedChain) BlockchainInfo(_ context.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	c.batchCalls.Add(1)
	c.mu.Lock()
	started, hold := c.batchStarted, c.batchHold
	c.mu.Unlock()
	if started != nil {
		c.batchOnce.Do(func() { close(started) })
	}
	if hold != nil {
		<-hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchRanges = append(c.batchRanges, [2]int64{minHeight, maxHeight})
	if maxHeight > c.tip {
		maxHeight = c.tip
	}
	if minHeight > maxHeight {
		return nil, errors.New("min height can't be greater than max height")
	}
	res := &ctypes.ResultBlockchainInfo{LastHeight: c.tip}
	for h := maxHeight; h >= minHeight; h-- {
		sh := c.headers[h]
		hdr := *sh.Header
		if c.tamperTime[h] {
			hdr.Time = hdr.Time.Add(-time.Second)
		}
		res.BlockMetas = append(res.BlockMetas, &cmttypes.BlockMeta{BlockID: sh.Commit.BlockID, Header: hdr})
	}
	return res, nil
}

func (c *SignedChain) ranges() [][2]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]int64(nil), c.batchRanges...)
}

func (c *SignedChain) header(height int64) *cmttypes.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := *c.headers[height].Header
	return &h
}

// HeaderAt returns a copy of the header at height.
func (c *SignedChain) HeaderAt(height int64) *cmttypes.Header {
	return c.header(height)
}

// HashOnly is the hash-only header at height.
func (c *SignedChain) HashOnly(height int64) *blocks.Header {
	h := c.header(height)
	return blocks.HashOnlyHeader(h.Height, h.Time, h.ChainID, h.Hash())
}

// BlockOf encodes hdr as a dapi BLOCK payload.
func BlockOf(hdr *cmttypes.Header) DAPIBlock {
	pb := hdr.ToProto()
	raw, err := pb.Marshal()
	if err != nil {
		panic(err)
	}
	return DAPIBlock{Height: hdr.Height, Hash: hdr.Hash(), TimeMs: hdr.Time.UnixMilli(), Header: raw}
}

// Block is the honest dapi BLOCK at height.
func (c *SignedChain) Block(height int64) DAPIBlock {
	return BlockOf(c.header(height))
}

// Page is the honest dapi BLOCKs from..to, skipping skip.
func (c *SignedChain) Page(from, to int64, skip ...int64) []DAPIBlock {
	var out []DAPIBlock
	for h := from; h <= to; h++ {
		missing := false
		for _, s := range skip {
			missing = missing || s == h
		}
		if !missing {
			out = append(out, c.Block(h))
		}
	}
	return out
}

// Forged is a header at height whose hash does not match the chain.
func (c *SignedChain) Forged(height int64) *cmttypes.Header {
	h := c.header(height)
	h.AppHash = tmhash.Sum([]byte("forged"))
	return h
}

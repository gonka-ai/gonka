package observer

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
	"github.com/stretchr/testify/require"
)

const signedChainID = "gonka-verify"

type signedChain struct {
	t       *testing.T
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
}

func newSignedChain(t *testing.T, tip int64) *signedChain {
	vals, privs := cmttypes.RandValidatorSet(4, 10)
	c := &signedChain{
		t: t, vals: vals, privs: privs,
		base:         time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond),
		headers:      make(map[int64]*cmttypes.SignedHeader),
		tamperTime:   make(map[int64]bool),
		forgeCommits: make(map[int64]bool),
	}
	c.grow(tip)
	return c
}

func (c *signedChain) blockTime(height int64) time.Time {
	return c.base.Add(time.Duration(height) * 5 * time.Second)
}

func (c *signedChain) grow(tip int64) {
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

func (c *signedChain) sign(height int64, last cmttypes.BlockID, vals *cmttypes.ValidatorSet, privs []cmttypes.PrivValidator) *cmttypes.SignedHeader {
	return c.signWithNext(height, last, vals, privs, vals)
}

func (c *signedChain) signWithNext(height int64, last cmttypes.BlockID, vals *cmttypes.ValidatorSet, privs []cmttypes.PrivValidator, next *cmttypes.ValidatorSet) *cmttypes.SignedHeader {
	hdr := &cmttypes.Header{
		Version:            cmtversion.Consensus{Block: version.BlockProtocol},
		ChainID:            signedChainID,
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
			require.NoError(c.t, err)
			if bytes.Equal(pk.Address(), v.Address) {
				signer = pv
			}
		}
		require.NotNil(c.t, signer)
		require.NoError(c.t, signer.SignVote(signedChainID, pb))
		vote.Signature = pb.Signature
		sigs[i] = vote.CommitSig()
	}
	return &cmttypes.SignedHeader{Header: hdr, Commit: &cmttypes.Commit{Height: height, Round: 0, BlockID: blockID, Signatures: sigs}}
}

func (c *signedChain) Header(_ context.Context, height *int64) (*ctypes.ResultHeader, error) {
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

func (c *signedChain) Commit(_ context.Context, height *int64) (*ctypes.ResultCommit, error) {
	c.commitCalls.Add(1)
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
	if c.forgeCommits[h] {
		forgedVals, forgedPrivs := cmttypes.RandValidatorSet(4, 10)
		forged := c.sign(h, sh.LastBlockID, forgedVals, forgedPrivs)
		return &ctypes.ResultCommit{SignedHeader: *forged}, nil
	}
	return &ctypes.ResultCommit{SignedHeader: *sh, CanonicalCommit: true}, nil
}

func (c *signedChain) Validators(_ context.Context, height *int64, page, perPage *int) (*ctypes.ResultValidators, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if height != nil && c.forgeCommits[*height] {
		return nil, errors.New("validators withheld")
	}
	return &ctypes.ResultValidators{BlockHeight: *height, Validators: c.vals.Copy().Validators, Count: c.vals.Size(), Total: c.vals.Size()}, nil
}

func (c *signedChain) BlockchainInfo(_ context.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error) {
	c.batchCalls.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
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

type memStore struct {
	mu      sync.Mutex
	headers map[int64]StoredHeader
	trust   *TrustPoint
}

func newMemStore() *memStore { return &memStore{headers: make(map[int64]StoredHeader)} }

func (m *memStore) LoadHeaders(_ context.Context, from int64) ([]StoredHeader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredHeader
	for h, r := range m.headers {
		if h >= from {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) SaveHeaders(_ context.Context, rows []StoredHeader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		m.headers[r.Height] = r
	}
	return nil
}

func (m *memStore) PruneHeaders(_ context.Context, below int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h := range m.headers {
		if h < below {
			delete(m.headers, h)
		}
	}
	return nil
}

func (m *memStore) LoadTrust(context.Context) (TrustPoint, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.trust == nil {
		return TrustPoint{}, false, nil
	}
	return *m.trust, true, nil
}

func (m *memStore) SaveTrust(_ context.Context, tp TrustPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trust = &tp
	return nil
}

func (m *memStore) MaxHeight(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var top int64
	for h := range m.headers {
		top = max(top, h)
	}
	return top, nil
}

func (m *memStore) has(height int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.headers[height]
	return ok
}

func verifyingOracle(c *signedChain, store HeaderStore) *Oracle {
	return newOracleWith(c, oracleOptions{verify: true, store: store})
}

func TestVerifiedAtProvesNewHeightsWithCommitSignatures(t *testing.T) {
	c := newSignedChain(t, 100)
	o := verifyingOracle(c, nil)
	ctx := context.Background()

	h, err := o.At(ctx, 100)
	require.NoError(t, err)
	require.True(t, c.blockTime(100).Equal(h.Time))

	c.grow(130)
	h, err = o.At(ctx, 130)
	require.NoError(t, err, "a skip of 30 heights is verified against the trusted validator set")
	require.Equal(t, c.headers[130].Hash().Bytes(), h.BlockHash)
	require.True(t, c.blockTime(130).Equal(h.Time))

	_, err = o.At(ctx, 131)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound, "a height the chain has not produced is not found")
}

func TestVerifiedAtWalksOlderHeightsDownTheHashChain(t *testing.T) {
	c := newSignedChain(t, 200)
	o := verifyingOracle(c, nil)
	ctx := context.Background()

	h, err := o.At(ctx, 150)
	require.NoError(t, err)
	require.True(t, c.blockTime(150).Equal(h.Time))
	require.Equal(t, c.headers[150].Hash().Bytes(), h.BlockHash)
	require.Equal(t, int64(3), c.batchCalls.Load(), "50 heights below the trusted tip take three batches")
	for height := int64(150); height <= 200; height++ {
		_, ok := o.history.Get(height)
		require.Truef(t, ok, "height %d is kept once proven", height)
	}
}

func TestVerifiedAtRefusesAHeaderWhoseTimeWasChanged(t *testing.T) {
	c := newSignedChain(t, 200)
	c.tamperTime[190] = true
	o := verifyingOracle(c, nil)

	_, err := o.At(context.Background(), 185)
	require.ErrorIs(t, err, ErrUnverifiedHeader)
	_, ok := o.history.Get(190)
	require.False(t, ok, "the altered header is not stored")
	_, ok = o.history.Get(185)
	require.False(t, ok, "nothing below the broken link is stored")
	_, ok = o.history.Get(191)
	require.True(t, ok, "the proven headers above the break stay")
}

func TestVerifiedAtRefusesAHeaderSignedByAnotherValidatorSet(t *testing.T) {
	c := newSignedChain(t, 100)
	o := verifyingOracle(c, nil)
	ctx := context.Background()
	_, err := o.At(ctx, 100)
	require.NoError(t, err)

	c.grow(101)
	c.mu.Lock()
	c.forgeCommits[101] = true
	c.mu.Unlock()
	_, err = o.At(ctx, 101)
	require.Error(t, err, "a node cannot swap in its own validators")
	_, ok := o.history.Get(101)
	require.False(t, ok)
}

func TestAtWaitReturnsTheHeaderOnceTheChainProducesIt(t *testing.T) {
	c := newSignedChain(t, 100)
	o := verifyingOracle(c, nil)
	ctx := context.Background()
	_, err := o.At(ctx, 100)
	require.NoError(t, err)

	go func() {
		time.Sleep(300 * time.Millisecond)
		c.grow(101)
	}()
	began := time.Now()
	h, err := o.AtWait(ctx, 101, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, int64(101), h.Height)
	require.Less(t, time.Since(began), 3*time.Second)

	began = time.Now()
	_, err = o.AtWait(ctx, 500, 700*time.Millisecond)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
	require.GreaterOrEqual(t, time.Since(began), 600*time.Millisecond, "a missing height waits the whole budget")
}

func TestHistoryAndTrustSurviveARestart(t *testing.T) {
	c := newSignedChain(t, 200)
	store := newMemStore()
	o := verifyingOracle(c, store)
	ctx := context.Background()
	_, err := o.At(ctx, 180)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return store.has(180) && store.has(200) }, 2*time.Second, 10*time.Millisecond)
	tp, ok, err := store.LoadTrust(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(200), tp.Height)

	commits := c.commitCalls.Load()
	batches := c.batchCalls.Load()
	restarted := verifyingOracle(c, store)
	h, err := restarted.At(ctx, 180)
	require.NoError(t, err)
	require.True(t, c.blockTime(180).Equal(h.Time))
	require.Equal(t, batches, c.batchCalls.Load(), "a stored height is served without asking the node")
	require.Equal(t, commits, c.commitCalls.Load())

	c.grow(205)
	_, err = restarted.At(ctx, 205)
	require.NoError(t, err, "the pinned trust point verifies the next heights after a restart")
}

func TestObserveVerifiesTipsInTheBackground(t *testing.T) {
	c := newSignedChain(t, 100)
	o := verifyingOracle(c, nil)
	c.grow(110)
	for h := int64(101); h <= 110; h++ {
		o.Observe(blocks.HashOnlyHeader(h, c.blockTime(h), signedChainID, c.headers[h].Hash()))
	}
	require.Eventually(t, func() bool {
		_, ok := o.history.Get(110)
		return ok
	}, 3*time.Second, 10*time.Millisecond)
	_, err := o.At(context.Background(), 105)
	require.NoError(t, err)
}

type countingValidators struct {
	*signedChain
	calls atomic.Int64
}

func (c *countingValidators) Validators(ctx context.Context, height *int64, page, perPage *int) (*ctypes.ResultValidators, error) {
	c.calls.Add(1)
	return c.signedChain.Validators(ctx, height, page, perPage)
}

func TestVerifyReusesAnUnchangedValidatorSet(t *testing.T) {
	c := &countingValidators{signedChain: newSignedChain(t, 100)}
	o := newOracleWith(c, oracleOptions{verify: true})
	ctx := context.Background()
	_, err := o.At(ctx, 100)
	require.NoError(t, err)
	before := c.calls.Load()
	c.grow(110)
	for h := int64(101); h <= 110; h++ {
		_, err := o.At(ctx, h)
		require.NoError(t, err)
	}
	require.Equal(t, before, c.calls.Load(), "the validator set is fetched only when its hash changes")
}

func TestRestartKeepsATrustPinTheNodeDoesNotMatch(t *testing.T) {
	honest := newSignedChain(t, 100)
	store := newMemStore()
	o := verifyingOracle(honest, store)
	_, err := o.At(context.Background(), 100)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, ok, _ := store.LoadTrust(context.Background()); return ok }, 2*time.Second, 10*time.Millisecond)
	pinned, _, _ := store.LoadTrust(context.Background())

	forged := newSignedChain(t, 150)
	store.mu.Lock()
	store.headers = make(map[int64]StoredHeader)
	store.mu.Unlock()
	restarted := verifyingOracle(forged, store)
	_, err = restarted.At(context.Background(), 150)
	require.ErrorIs(t, err, ErrUnverifiedHeader, "a node whose chain does not contain the pinned block is not trusted")
	_, err = restarted.At(context.Background(), 150)
	require.ErrorIs(t, err, ErrUnverifiedHeader, "the pin is kept, so a retry refuses again")
	tp, _, _ := store.LoadTrust(context.Background())
	require.Equal(t, pinned, tp)

	reset := newOracleWith(forged, oracleOptions{verify: true, store: store, resetTrust: true})
	_, err = reset.At(context.Background(), 150)
	require.NoError(t, err, "an operator reset starts a new trust root")
}

func TestVerifyModeIgnoresRowsStoredWithoutVerification(t *testing.T) {
	c := newSignedChain(t, 100)
	store := newMemStore()
	tampered := c.blockTime(90).Add(10 * time.Minute)
	require.NoError(t, store.SaveHeaders(context.Background(), []StoredHeader{
		{Height: 90, Hash: c.headers[90].Hash().Bytes(), TimeMs: tampered.UnixMilli(), ChainID: signedChainID},
	}))
	o := verifyingOracle(c, store)
	h, err := o.At(context.Background(), 90)
	require.NoError(t, err)
	require.True(t, c.blockTime(90).Equal(h.Time), "the unverified row's time is not served")
	require.Positive(t, c.commitCalls.Load())
}

func TestVerifiedAtReachesHeightsOlderThanTheWindow(t *testing.T) {
	c := newSignedChain(t, 200)
	o := newOracleWith(c, oracleOptions{verify: true, historyHeights: 5})
	ctx := context.Background()

	h, err := o.At(ctx, 30)
	require.NoError(t, err)
	require.True(t, c.blockTime(30).Equal(h.Time))
	batches := c.batchCalls.Load()
	require.LessOrEqual(t, batches, int64(10), "170 heights take nine batches")

	h, err = o.At(ctx, 25)
	require.NoError(t, err)
	require.True(t, c.blockTime(25).Equal(h.Time))
	require.Equal(t, batches+1, c.batchCalls.Load(), "the walk resumes from the last proven old height")
}

type flakyTrustStore struct {
	*memStore
	failReads atomic.Bool
	saves     atomic.Int64
}

func (f *flakyTrustStore) LoadTrust(ctx context.Context) (TrustPoint, bool, error) {
	if f.failReads.Load() {
		return TrustPoint{}, false, errors.New("database is locked")
	}
	return f.memStore.LoadTrust(ctx)
}

func (f *flakyTrustStore) SaveTrust(ctx context.Context, tp TrustPoint) error {
	f.saves.Add(1)
	return f.memStore.SaveTrust(ctx, tp)
}

func TestAnUnreadableTrustStoreBlocksBootstrapInsteadOfRetrusting(t *testing.T) {
	honest := newSignedChain(t, 100)
	base := newMemStore()
	first := verifyingOracle(honest, base)
	_, err := first.At(context.Background(), 100)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, ok, _ := base.LoadTrust(context.Background()); return ok }, 2*time.Second, 10*time.Millisecond)
	pinned, _, _ := base.LoadTrust(context.Background())

	store := &flakyTrustStore{memStore: base}
	store.failReads.Store(true)
	forged := newSignedChain(t, 150)
	restarted := verifyingOracle(forged, store)
	_, err = restarted.At(context.Background(), 150)
	require.ErrorContains(t, err, "read trust point")
	require.Zero(t, store.saves.Load(), "nothing overwrites the pin while it cannot be read")

	store.failReads.Store(false)
	_, err = restarted.At(context.Background(), 150)
	require.ErrorIs(t, err, ErrUnverifiedHeader, "once readable, the pin still refuses the other fork")
	tp, _, _ := base.LoadTrust(context.Background())
	require.Equal(t, pinned, tp)
}

func TestTrustResetDoesNotServeHeadersFromTheOldRoot(t *testing.T) {
	first := newSignedChain(t, 100)
	store := newMemStore()
	o := verifyingOracle(first, store)
	_, err := o.At(context.Background(), 60)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return store.has(60) }, 2*time.Second, 10*time.Millisecond)

	second := newSignedChain(t, 120)
	reset := newOracleWith(second, oracleOptions{verify: true, store: store, resetTrust: true})
	h, err := reset.At(context.Background(), 60)
	require.NoError(t, err)
	require.Equal(t, second.headers[60].Hash().Bytes(), h.BlockHash, "height 60 comes from the new root, not the old cache")
	tp, ok, err := store.LoadTrust(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(1), tp.Epoch)

	again := verifyingOracle(second, store)
	h, err = again.At(context.Background(), 60)
	require.NoError(t, err)
	require.Equal(t, second.headers[60].Hash().Bytes(), h.BlockHash, "the next restart keeps the new epoch")
}

func TestVerifiedAtFollowsIntermediateHeadersAcrossValidatorRotation(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "valid transition"
		if !valid {
			name = "uncommitted transition"
		}
		t.Run(name, func(t *testing.T) {
			c := newSignedChain(t, 1)
			nextVals, nextPrivs := cmttypes.RandValidatorSet(4, 10)
			if valid {
				c.headers[1] = c.signWithNext(1, cmttypes.BlockID{}, c.vals, c.privs, nextVals)
			}
			o := verifyingOracle(c, nil)
			_, err := o.At(context.Background(), 1)
			require.NoError(t, err)
			c.vals, c.privs = nextVals, nextPrivs
			c.grow(5)
			header, err := o.At(context.Background(), 5)
			if !valid {
				require.ErrorIs(t, err, blocks.ErrHeaderUnverified)
				require.Nil(t, header)
				require.EqualValues(t, 1, o.verifier.trustedHeight())
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.headers[5].Hash().Bytes(), header.BlockHash)
			require.EqualValues(t, 5, o.verifier.trustedHeight())
		})
	}
}

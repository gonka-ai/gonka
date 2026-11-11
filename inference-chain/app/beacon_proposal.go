package app

// Block BLS beacon, Step 4 (proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md):
// block h+1 must carry, as Txs[0], either σ_h (any t+1 valid partials
// aggregate to the same signature, checked with one pairing) or a proof of
// miss (the extended commit of h, holding fewer than t+1 valid slots). A
// block with neither is rejected, so the next proposer takes over.
//
// Partials are collected off the consensus path by beaconCollector from the
// node's own ExtendVote and every peer's VerifyVoteExtension, so a proposer
// normally has σ ready. PrepareProposal falls back to the partials in its
// LocalLastCommit, and builds the proof of miss from it when they fall short.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/app/beacon"
	"github.com/productscience/inference/x/bls/blsutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
)

// beaconChain is the committed beacon state the proposal path reads (x/bls keeper).
type beaconChain interface {
	BeaconInjectionRound(ctx sdk.Context, proposalHeight int64) (blstypes.PendingBeacon, bool)
	GetPendingBeacon(ctx sdk.Context, round uint64) (blstypes.PendingBeacon, bool)
	ValidateBeaconExtensionStructure(ctx sdk.Context, ext []byte, round uint64, consAddr []byte) (*blstypes.BeaconVoteExtension, error)
	GetBeaconKeyMaterial(ctx sdk.Context, epoch uint64) (blskeeper.BeaconKeyMaterial, error)
}

// ---------------------------------------------------------------------------
// Partial verification (shared by the collector, PrepareProposal and ProcessProposal)
// ---------------------------------------------------------------------------

// Verdicts of one extension (label values of beacon_collector_partials_total).
const (
	partialValid     = "valid"
	partialInvalid   = "invalid"
	partialNotOwner  = "not_owner"
	partialEmpty     = "empty"
	partialDuplicate = "duplicate"
)

// beaconPartials accumulates the valid per-slot partials of one round.
type beaconPartials struct {
	round   uint64
	pending blstypes.PendingBeacon
	key     blskeeper.BeaconKeyMaterial
	slots   map[uint32][]byte
	seen    map[string]bool
}

func newBeaconPartials(pending blstypes.PendingBeacon, key blskeeper.BeaconKeyMaterial) *beaconPartials {
	return &beaconPartials{round: pending.Round, pending: pending, key: key,
		slots: map[uint32][]byte{}, seen: map[string]bool{}}
}

// add checks one validator's extension (structure, ownership, each slot's
// pairing) and keeps its valid slots. Bad content only counts as zero (R2).
func (p *beaconPartials) add(ctx sdk.Context, chain beaconChain, consAddr, ext []byte) string {
	if len(ext) == 0 {
		return partialEmpty
	}
	if p.seen[string(consAddr)] {
		return partialDuplicate
	}
	p.seen[string(consAddr)] = true
	ve, err := chain.ValidateBeaconExtensionStructure(ctx, ext, p.round, consAddr)
	if err != nil {
		if errors.Is(err, blskeeper.ErrBeaconNoOwner) {
			return partialNotOwner
		}
		return partialInvalid
	}
	valid := 0
	for i, slot := range ve.SlotIndices {
		if _, have := p.slots[slot]; have || int(slot) >= len(p.key.SlotPublicKeys) {
			continue
		}
		sig := ve.Signatures[i*blsutil.SignatureLen : (i+1)*blsutil.SignatureLen]
		if blsutil.VerifyPartial(p.pending.Msg, sig, p.key.SlotPublicKeys[slot]) {
			p.slots[slot] = append([]byte(nil), sig...)
			valid++
		}
	}
	if valid == 0 {
		return partialInvalid
	}
	return partialValid
}

func (p *beaconPartials) enough() bool { return uint32(len(p.slots)) >= p.key.Threshold }

// sigma aggregates the collected slots and checks the result against the group key.
func (p *beaconPartials) sigma() ([]byte, error) {
	slots := make([]uint32, 0, len(p.slots))
	for s := range p.slots {
		slots = append(slots, s)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	sigs := make([][]byte, len(slots))
	for i, s := range slots {
		sigs[i] = p.slots[s]
	}
	sigma, err := blsutil.AggregatePartials(slots, sigs)
	if err != nil {
		return nil, err
	}
	if !blsutil.VerifySignature(p.pending.Msg, sigma, p.key.GroupPublicKey) {
		return nil, fmt.Errorf("aggregated σ for round %d does not verify", p.round)
	}
	return sigma, nil
}

// bitmap is the contributors hint: bit s set for each collected slot.
func (p *beaconPartials) bitmap() []byte {
	n := len(p.key.SlotPublicKeys)
	out := make([]byte, (n+7)/8)
	for s := range p.slots {
		if int(s) < n {
			out[s/8] |= 1 << (s % 8)
		}
	}
	return out
}

// countCommitPartials verifies the partials in the commit votes of ec (votes
// with BlockIdFlagCommit only) until the threshold is reached.
func countCommitPartials(ctx sdk.Context, chain beaconChain, pending blstypes.PendingBeacon, key blskeeper.BeaconKeyMaterial, ec abci.ExtendedCommitInfo) *beaconPartials {
	p := newBeaconPartials(pending, key)
	for _, v := range ec.Votes {
		if v.BlockIdFlag != cmtproto.BlockIDFlagCommit {
			continue
		}
		p.add(ctx, chain, v.Validator.Address, v.VoteExtension)
		if p.enough() {
			break
		}
	}
	return p
}

// ---------------------------------------------------------------------------
// Collector
// ---------------------------------------------------------------------------

const (
	collectorQueueSize  = 4096
	collectorKeepRounds = 4
)

type collectItem struct {
	height int64
	addr   []byte
	ext    []byte
}

type collectRound struct {
	ctx      sdk.Context
	partials *beaconPartials
	sigma    []byte
	hint     []byte
	dead     bool // no pending beacon or key material for this round
}

// beaconCollector verifies partials as they arrive and builds σ per round.
type beaconCollector struct {
	logger log.Logger
	chain  beaconChain
	// ctxAt returns a read-only context over committed version v. Round h's
	// PendingBeacon is written in EndBlock(h-1), so items of round h are
	// checked against version h-1, committed before anyone votes on h.
	ctxAt func(version int64) (sdk.Context, error)

	queue chan collectItem
	// inflight counts queued and in-process items (tests wait for zero before
	// writing state directly, see WaitBeaconCollectorIdleForTest).
	inflight atomic.Int64

	mu     sync.Mutex
	rounds map[uint64]*collectRound
	newest uint64

	cancel context.CancelFunc
	done   chan struct{}
}

func newBeaconCollector(logger log.Logger, chain beaconChain, ctxAt func(int64) (sdk.Context, error)) *beaconCollector {
	return &beaconCollector{logger: logger, chain: chain, ctxAt: ctxAt,
		queue: make(chan collectItem, collectorQueueSize), rounds: map[uint64]*collectRound{}}
}

// Add queues an extension for verification. It never blocks: a full queue
// drops the item (the proposer still has its LocalLastCommit).
func (c *beaconCollector) Add(height int64, consAddr, ext []byte) {
	if c == nil || height <= 1 || len(ext) == 0 || len(consAddr) == 0 {
		return
	}
	it := collectItem{height: height, addr: append([]byte(nil), consAddr...), ext: append([]byte(nil), ext...)}
	c.inflight.Add(1)
	select {
	case c.queue <- it:
	default:
		c.inflight.Add(-1)
		beacon.CollectorPartials.WithLabelValues("dropped").Inc()
	}
}

// Sigma returns σ for round if the collector built it with keyEpoch's key.
func (c *beaconCollector) Sigma(round, keyEpoch uint64) (sigma, hint []byte, ok bool) {
	if c == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.rounds[round]
	if r == nil || r.sigma == nil || r.partials.pending.KeyEpoch != keyEpoch {
		return nil, nil, false
	}
	return r.sigma, r.hint, true
}

func (c *beaconCollector) start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		for {
			select {
			case <-ctx.Done():
				return
			case it := <-c.queue:
				c.safeProcess(it)
				c.inflight.Add(-1)
			}
		}
	}()
}

func (c *beaconCollector) close() {
	if c != nil && c.cancel != nil {
		c.cancel()
		<-c.done
	}
}

func (c *beaconCollector) safeProcess(it collectItem) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("beacon collector panicked", "height", it.height, "panic", r)
		}
	}()
	c.process(it)
}

// process runs on the collector goroutine only; c.mu guards what Sigma reads.
func (c *beaconCollector) process(it collectItem) {
	round := uint64(it.height)
	c.mu.Lock()
	if round+collectorKeepRounds < c.newest {
		c.mu.Unlock()
		beacon.CollectorPartials.WithLabelValues("stale").Inc()
		return
	}
	if round > c.newest {
		c.newest = round
		for r := range c.rounds {
			if r+collectorKeepRounds < round {
				delete(c.rounds, r)
			}
		}
	}
	r := c.rounds[round]
	c.mu.Unlock()

	if r == nil {
		r = &collectRound{}
		ctx, err := c.ctxAt(it.height - 1)
		pending, ok := blstypes.PendingBeacon{}, false
		if err == nil {
			pending, ok = c.chain.GetPendingBeacon(ctx, round)
		}
		var key blskeeper.BeaconKeyMaterial
		if ok {
			key, err = c.chain.GetBeaconKeyMaterial(ctx, pending.KeyEpoch)
		}
		if err != nil || !ok {
			r.dead = true
		} else {
			r.ctx, r.partials = ctx, newBeaconPartials(pending, key)
		}
		c.mu.Lock()
		c.rounds[round] = r
		c.mu.Unlock()
	}
	if r.dead {
		beacon.CollectorPartials.WithLabelValues("no_pending").Inc()
		return
	}
	if r.sigma != nil {
		return // done for this round
	}
	verdict := r.partials.add(r.ctx, c.chain, it.addr, it.ext)
	beacon.CollectorPartials.WithLabelValues(verdict).Inc()
	if verdict == partialInvalid || verdict == partialNotOwner {
		c.logger.Info("beacon partials rejected", "round", round, "validator", fmt.Sprintf("%X", it.addr), "reason", verdict)
	}
	if !r.partials.enough() {
		return
	}
	sigma, err := r.partials.sigma()
	if err != nil {
		c.logger.Error("beacon σ aggregation failed", "round", round, "err", err)
		return
	}
	c.mu.Lock()
	r.sigma, r.hint = sigma, r.partials.bitmap()
	c.mu.Unlock()
	beacon.SigmaBuilt.Inc()
}

// ---------------------------------------------------------------------------
// PrepareProposal (R4: reserved leading tx, mempool pass-through on req.Txs only)
// ---------------------------------------------------------------------------

// FlagBeaconTestWithholdSigma is a TEST-ONLY switch ([bls-beacon]
// test-withhold-sigma, env INFERENCED_BLS_BEACON_TEST_WITHHOLD_SIGMA): this node
// proposes blocks without the beacon tx even when one is required, so the
// other validators reject its proposals and CometBFT moves to the next
// proposer. It only ever hurts the node that sets it. Not in the app.toml template.
const FlagBeaconTestWithholdSigma = "bls-beacon.test-withhold-sigma"

func newBeaconPrepareProposalHandler(logger log.Logger, chain beaconChain, collector *beaconCollector, inner sdk.PrepareProposalHandler, withholdSigma ...bool) sdk.PrepareProposalHandler {
	withhold := len(withholdSigma) > 0 && withholdSigma[0]
	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (resp *abci.ResponsePrepareProposal, err error) {
		withExt, total := countVoteExtensions(req.LocalLastCommit)
		logger.Debug("prepare proposal", "height", req.Height,
			"last_commit_votes", total, "last_commit_votes_with_extension", withExt)

		var reserved []byte
		func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("panic building the beacon tx; proposing without it", "height", req.Height, "panic", fmt.Sprint(r))
					reserved = nil
				}
			}()
			if chain == nil {
				return
			}
			pending, required := chain.BeaconInjectionRound(ctx, req.Height)
			if !required {
				return
			}
			if withhold {
				beacon.ProposalOutcome.WithLabelValues("withheld").Inc()
				logger.Warn("TEST-ONLY: proposing without the required beacon tx", "height", req.Height, "flag", FlagBeaconTestWithholdSigma)
				return
			}
			reserved = buildBeaconTx(ctx, logger, chain, collector, pending, req)
		}()
		return selectProposalTxs(ctx, logger, inner, req, reserved)
	}
}

// buildBeaconTx returns σ for the previous round if the collector or the
// LocalLastCommit reaches the threshold, else a proof of miss.
func buildBeaconTx(ctx sdk.Context, logger log.Logger, chain beaconChain, collector *beaconCollector, pending blstypes.PendingBeacon, req *abci.RequestPrepareProposal) []byte {
	round := uint64(req.Height - 1)
	sigmaTx := func(sigma, hint []byte) []byte {
		tx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Sigma{Sigma: &blstypes.BeaconSigma{
			Round: round, KeyEpoch: pending.KeyEpoch, Sigma: sigma, ContributorsHint: hint,
		}}})
		if err != nil {
			return nil
		}
		beacon.ProposalOutcome.WithLabelValues("sigma").Inc()
		return tx
	}
	if sigma, hint, ok := collector.Sigma(round, pending.KeyEpoch); ok {
		return sigmaTx(sigma, hint)
	}
	start := time.Now()
	if key, err := chain.GetBeaconKeyMaterial(ctx, pending.KeyEpoch); err == nil {
		p := countCommitPartials(ctx, chain, pending, key, req.LocalLastCommit)
		if p.enough() {
			if sigma, err := p.sigma(); err == nil {
				logger.Debug("beacon σ built from LocalLastCommit", "round", round, "took", time.Since(start).String())
				return sigmaTx(sigma, p.bitmap())
			}
		}
	}
	ec, err := req.LocalLastCommit.Marshal()
	if err != nil {
		logger.Error("cannot encode the extended commit for a proof of miss", "round", round, "err", err)
		return nil
	}
	tx, err := blstypes.EncodeBeaconTx(&blstypes.InjectedBeacon{Outcome: &blstypes.InjectedBeacon_Miss{Miss: &blstypes.BeaconMissProof{
		Round: round, KeyEpoch: pending.KeyEpoch, ExtendedCommit: ec,
	}}})
	if err != nil {
		return nil
	}
	beacon.ProposalOutcome.WithLabelValues("miss").Inc()
	logger.Info("beacon round below threshold; proposing a proof of miss", "round", round, "key_epoch", pending.KeyEpoch)
	return tx
}

// selectProposalTxs runs the mempool pass-through on req.Txs (without any
// beacon-prefixed tx) within the byte budget left after reserved, and puts
// reserved first. reserved is never dropped for space; a pass-through error
// yields [reserved] (R4).
func selectProposalTxs(ctx sdk.Context, logger log.Logger, inner sdk.PrepareProposalHandler, req *abci.RequestPrepareProposal, reserved []byte) (*abci.ResponsePrepareProposal, error) {
	sub := *req
	sub.Txs = make([][]byte, 0, len(req.Txs))
	for _, tx := range req.Txs {
		if !blstypes.IsBeaconTx(tx) {
			sub.Txs = append(sub.Txs, tx)
		}
	}
	if reserved == nil {
		return inner(ctx, &sub)
	}
	sub.MaxTxBytes = req.MaxTxBytes - cmttypes.ComputeProtoSizeForTxs([]cmttypes.Tx{reserved})
	out := [][]byte{reserved}
	if sub.MaxTxBytes <= 0 {
		return &abci.ResponsePrepareProposal{Txs: out}, nil
	}
	resp, err := inner(ctx, &sub)
	if err != nil {
		logger.Error("mempool pass-through failed; proposing the beacon tx alone", "height", req.Height, "err", err)
		return &abci.ResponsePrepareProposal{Txs: out}, nil
	}
	for _, tx := range resp.Txs {
		if !blstypes.IsBeaconTx(tx) {
			out = append(out, tx)
		}
	}
	return &abci.ResponsePrepareProposal{Txs: out}, nil
}

// ---------------------------------------------------------------------------
// ProcessProposal (R5: only Txs[0] is interpreted; Txs[1:] never decoded)
// ---------------------------------------------------------------------------

// voteExtValidator checks the extension signatures of an extended commit
// against the proposal's LastCommit (baseapp.ValidateVoteExtensions).
type voteExtValidator func(ctx sdk.Context, ec abci.ExtendedCommitInfo) error

func newBeaconProcessProposalHandler(logger log.Logger, chain beaconChain, validateExt voteExtValidator) sdk.ProcessProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestProcessProposal) (resp *abci.ResponseProcessProposal, err error) {
		verdict := func(accept bool, reason string) (*abci.ResponseProcessProposal, error) {
			result, status := "accept", abci.ResponseProcessProposal_ACCEPT
			if !accept {
				result, status = "reject", abci.ResponseProcessProposal_REJECT
				logger.Info("beacon process proposal reject", "height", req.Height, "reason", reason,
					"proposer", fmt.Sprintf("%X", req.ProposerAddress))
			}
			beacon.ProcessProposal.WithLabelValues(result, reason).Inc()
			return &abci.ResponseProcessProposal{Status: status}, nil
		}
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in beacon ProcessProposal; rejecting", "height", req.Height, "panic", fmt.Sprint(r))
				resp, err = verdict(false, "panic")
			}
		}()

		for i, tx := range req.Txs {
			if i > 0 && blstypes.IsBeaconTx(tx) {
				return verdict(false, "prefix_elsewhere")
			}
		}
		has := len(req.Txs) > 0 && blstypes.IsBeaconTx(req.Txs[0])
		var pending blstypes.PendingBeacon
		required := false
		if chain != nil {
			pending, required = chain.BeaconInjectionRound(ctx, req.Height)
		}
		if !required {
			if has {
				return verdict(false, "not_required")
			}
			return verdict(true, "no_beacon")
		}
		if !has {
			return verdict(false, "missing")
		}
		ib, err := blstypes.DecodeBeaconTx(req.Txs[0])
		if err != nil {
			return verdict(false, "decode")
		}
		round := uint64(req.Height - 1)
		switch o := ib.Outcome.(type) {
		case *blstypes.InjectedBeacon_Sigma:
			s := o.Sigma
			if s == nil || s.Round != round || s.KeyEpoch != pending.KeyEpoch {
				return verdict(false, "round_or_epoch")
			}
			key, err := chain.GetBeaconKeyMaterial(ctx, pending.KeyEpoch)
			if err != nil || !blsutil.VerifySignature(pending.Msg, s.Sigma, key.GroupPublicKey) {
				return verdict(false, "bad_sigma")
			}
			return verdict(true, "sigma")
		case *blstypes.InjectedBeacon_Miss:
			m := o.Miss
			if m == nil || m.Round != round || m.KeyEpoch != pending.KeyEpoch {
				return verdict(false, "round_or_epoch")
			}
			var ec abci.ExtendedCommitInfo
			if err := ec.Unmarshal(m.ExtendedCommit); err != nil {
				return verdict(false, "decode")
			}
			// Same votes as the block's LastCommit (addresses, powers, order
			// and round are also checked by ValidateVoteExtensions), and the
			// same flags, so no vote's extension can be hidden.
			if len(ec.Votes) != len(req.ProposedLastCommit.Votes) || ec.Round != req.ProposedLastCommit.Round {
				return verdict(false, "commit_mismatch")
			}
			for i, v := range ec.Votes {
				lc := req.ProposedLastCommit.Votes[i]
				if v.BlockIdFlag != lc.BlockIdFlag || string(v.Validator.Address) != string(lc.Validator.Address) ||
					v.Validator.Power != lc.Validator.Power {
					return verdict(false, "commit_mismatch")
				}
			}
			if validateExt == nil {
				return verdict(false, "no_ext_validator")
			}
			if err := validateExt(ctx, ec); err != nil {
				logger.Info("beacon proof of miss: extension signatures", "height", req.Height, "err", err)
				return verdict(false, "ext_signatures")
			}
			key, err := chain.GetBeaconKeyMaterial(ctx, pending.KeyEpoch)
			if err != nil {
				return verdict(true, "miss") // nobody can count: same state for every validator
			}
			if countCommitPartials(ctx, chain, pending, key, ec).enough() {
				return verdict(false, "miss_disproved")
			}
			return verdict(true, "miss")
		default:
			return verdict(false, "decode")
		}
	}
}

package app

// Block BLS beacon: ABCI handler skeleton (Step 1 of
// proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md).
//
// Step 1 installs explicit vote-extension, proposal and pre-block handlers that
// behave exactly like today's chain (empty extensions, pass-through proposals),
// so later steps only fill in the bodies. Invariants that every later step must
// preserve:
//
//   - ExtendVote / VerifyVoteExtension NEVER return an error. CometBFT 0.38
//     panics on either (state/execution.go: "ExtendVote call failed",
//     "VerifyVoteExtension call failed"). The SDK converts handler panics into
//     errors, so we must recover inside the handler ourselves.
//   - VerifyVoteExtension is tolerant of payloads it does not understand: it
//     accepts any extension up to a size cap and rejects only oversized ones.
//     Rejecting a peer's extension rejects its precommit, so a strict check
//     would turn any binary-version skew (patch release, rolling swap, the
//     first height of the upgrade that turns on real partials) into lost
//     precommits and possibly a halt. Content validation happens where it is
//     deterministic and non-fatal: each node's collector and, for a proof of
//     miss, ProcessProposal count bad partials as zero (beacon_proposal.go).
//   - Proposal handling is independent of the local app-side mempool setting
//     (`mempool.max-txs`): PrepareProposal always selects from req.Txs and
//     ProcessProposal never delegates to the SDK DefaultProposalHandler, so all
//     validators evaluate proposals identically regardless of app.toml.

import (
	"context"
	"fmt"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/server"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/spf13/cast"

	"github.com/productscience/inference/app/beacon"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
)

const beaconLogModule = "beacon"

// beaconExtensionMaxBytes is the largest vote extension accepted: enough for a
// validator that owns every BLS slot, nothing more. It bounds what a peer can
// make every node store in the extended commit. The formula lives in x/bls
// (keeper.MaxBeaconExtensionBytes) so FinalizeBeacon applies the same bound.
func beaconExtensionMaxBytes(totalSlots uint32) int {
	return blskeeper.MaxBeaconExtensionBytes(totalSlots)
}

// extensionSizeCap returns the vote-extension size cap for a height, from the
// committed state in ctx.
type extensionSizeCap func(ctx sdk.Context, height int64) int

// extensionBuilder produces the local vote extension for a height (Step 3:
// beaconExtensionBuilder, BLS partials over the pending beacon message).
type extensionBuilder func(ctx sdk.Context, req *abci.RequestExtendVote) ([]byte, error)

// preBlockRunner is the subset of the module manager the beacon PreBlocker needs.
type preBlockRunner interface {
	PreBlock(ctx sdk.Context) (*sdk.ResponsePreBlock, error)
}

// registerBeaconABCI installs the beacon ABCI handlers. It must run after
// appBuilder.Build and before app.Load: the BaseApp setters panic once sealed,
// and runtime only installs its default PreBlocker when none is set.
func (app *App) registerBeaconABCI(appOpts servertypes.AppOptions) {
	logger := app.Logger().With(log.ModuleKey, beaconLogModule)

	if maxTxs := cast.ToInt(appOpts.Get(server.FlagMempoolMaxTxs)); maxTxs >= 0 {
		logger.Warn("app-side mempool is enabled but ignored by beacon proposal handlers; proposals are built from CometBFT's mempool",
			"mempool.max-txs", maxTxs)
	}

	// Same selection as the SDK default handler with a NoOp mempool (decode,
	// byte/gas caps over req.Txs), regardless of the configured app mempool.
	passThrough := baseapp.NewDefaultProposalHandler(mempool.NoOpMempool{}, app.BaseApp).PrepareProposalHandler()

	// Step 2: load the dAPI-exported BLS shares (verified in the background).
	// Step 3: ExtendVote signs the round's pending beacon message with them.
	app.setupBeaconShares(appOpts, logger)
	// Step 4: the collector verifies partials off the consensus path (own
	// ExtendVote + peers' VerifyVoteExtension) so proposers have σ ready.
	app.beaconCollector = newBeaconCollector(logger, app.BlsKeeper, app.beaconContextAt)
	app.beaconCollector.start(context.Background())
	build := beaconExtensionBuilder(logger, app.BlsKeeper, func() beacon.ShareProvider { return app.BeaconShares })
	app.SetExtendVoteHandler(newBeaconExtendVoteHandler(logger, func(ctx sdk.Context, req *abci.RequestExtendVote) ([]byte, error) {
		ext, err := build(ctx, req)
		if err == nil && len(ext) > 0 && app.beaconIdentity != nil {
			if self, ok := app.beaconIdentity.ConsensusAddress(); ok {
				app.beaconCollector.Add(req.Height, self, ext)
			}
		}
		return ext, err
	}))
	sizeCap := beaconSizeCap(func(ctx sdk.Context) (blstypes.Params, error) { return app.BlsKeeper.GetParams(ctx) }, app.BlsKeeper)
	app.SetVerifyVoteExtensionHandler(newBeaconVerifyVoteExtensionHandler(logger, sizeCap, app.beaconCollector.Add))
	withhold := cast.ToBool(appOpts.Get(FlagBeaconTestWithholdSigma))
	if withhold {
		logger.Warn("TEST-ONLY: this node proposes blocks without the beacon tx; its proposals will be rejected",
			"flag", FlagBeaconTestWithholdSigma)
	}
	app.SetPrepareProposal(newBeaconPrepareProposalHandler(logger, app.BlsKeeper, app.beaconCollector, passThrough, withhold))
	app.SetProcessProposal(newBeaconProcessProposalHandler(logger, app.BlsKeeper, func(ctx sdk.Context, ec abci.ExtendedCommitInfo) error {
		return baseapp.ValidateVoteExtensions(ctx, app.StakingKeeper, 0, "", ec)
	}))
	// app.ModuleManager is resolved at call time; runtime sets the PreBlocker
	// order during Load, which is fine because the closure runs later.
	preBlock := newBeaconPreBlocker(moduleManagerPreBlock{app}, app.BlsKeeper.FinalizeBeacon)
	app.SetPreBlocker(func(ctx sdk.Context, req *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
		// FinalizeBlock(h) runs on top of committed version h-1: the share
		// verifier reads that immutable version in the background.
		app.beaconCommittedHeight.Store(ctx.BlockHeight() - 1)
		return preBlock(ctx, req)
	})
}

type moduleManagerPreBlock struct{ app *App }

func (m moduleManagerPreBlock) PreBlock(ctx sdk.Context) (*sdk.ResponsePreBlock, error) {
	return m.app.ModuleManager.PreBlock(ctx)
}

// newBeaconExtendVoteHandler returns an ExtendVote handler that never errors.
// Any failure or panic in build degrades to an empty extension.
func newBeaconExtendVoteHandler(logger log.Logger, build extensionBuilder) sdk.ExtendVoteHandler {
	return func(ctx sdk.Context, req *abci.RequestExtendVote) (resp *abci.ResponseExtendVote, err error) {
		empty := &abci.ResponseExtendVote{VoteExtension: []byte{}}
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in beacon ExtendVote; returning empty extension", "height", req.Height, "panic", fmt.Sprint(r))
				resp, err = empty, nil
			}
		}()

		if build == nil {
			return empty, nil
		}
		ext, buildErr := build(ctx, req)
		if buildErr != nil {
			logger.Error("beacon ExtendVote failed; returning empty extension", "height", req.Height, "err", buildErr)
			return empty, nil
		}
		if ext == nil {
			ext = []byte{}
		}
		return &abci.ResponseExtendVote{VoteExtension: ext}, nil
	}
}

// newBeaconVerifyVoteExtensionHandler accepts any extension up to sizeCap
// (empty included) and rejects only oversized ones. Payload content is not
// interpreted here; see the invariants at the top of this file. Never errors.
func newBeaconVerifyVoteExtensionHandler(logger log.Logger, sizeCap extensionSizeCap, onAccept func(height int64, addr, ext []byte)) sdk.VerifyVoteExtensionHandler {
	return func(ctx sdk.Context, req *abci.RequestVerifyVoteExtension) (resp *abci.ResponseVerifyVoteExtension, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in beacon VerifyVoteExtension; rejecting", "height", req.Height, "panic", fmt.Sprint(r))
				beacon.VerifyVoteExt.WithLabelValues("reject", "panic").Inc()
				resp, err = &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
			}
		}()

		n := len(req.VoteExtension)
		if n == 0 {
			beacon.VerifyVoteExt.WithLabelValues("accept", "empty").Inc()
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
		}
		if limit := sizeCap(ctx, req.Height); n > limit {
			beacon.VerifyVoteExt.WithLabelValues("reject", "oversized").Inc()
			logger.Info("rejecting oversized vote extension",
				"height", req.Height,
				"validator", fmt.Sprintf("%X", req.ValidatorAddress),
				"len", n,
				"max", limit)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}
		beacon.VerifyVoteExt.WithLabelValues("accept", "within_cap").Inc()
		if onAccept != nil {
			onAccept(req.Height, req.ValidatorAddress, req.VoteExtension) // non-blocking (collector queue)
		}
		return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
	}
}

// beaconFinalizer records the previous round from the block's txs (Step 5).
type beaconFinalizer func(ctx sdk.Context, txs [][]byte)

// newBeaconPreBlocker runs the module PreBlockers (x/upgrade first, so an
// upgrade at this height is applied), then records the beacon round h-1 from
// the injected beacon tx (Txs[0]). The beacon step never fails the block.
func newBeaconPreBlocker(mm preBlockRunner, finalize ...beaconFinalizer) sdk.PreBlocker {
	return func(ctx sdk.Context, req *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
		resp, err := mm.PreBlock(ctx)
		if err != nil || len(finalize) == 0 || finalize[0] == nil {
			return resp, err
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					ctx.Logger().Error("panic recording the beacon round; block continues", "height", req.Height, "panic", fmt.Sprint(r))
				}
			}()
			finalize[0](ctx, req.Txs)
		}()
		return resp, nil
	}
}

func countVoteExtensions(ec abci.ExtendedCommitInfo) (withExtension, total int) {
	for _, v := range ec.Votes {
		total++
		if len(v.VoteExtension) > 0 {
			withExtension++
		}
	}
	return withExtension, total
}

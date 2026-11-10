package app

// Block BLS beacon: ABCI handler skeleton (Step 1 of
// devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md).
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
//     deterministic and non-fatal: ProcessProposal / FinalizeBlock (Steps 4–5).
//   - Proposal handling is independent of the local app-side mempool setting
//     (`mempool.max-txs`): PrepareProposal always selects from req.Txs and
//     ProcessProposal never delegates to the SDK DefaultProposalHandler, so all
//     validators evaluate proposals identically regardless of app.toml.

import (
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
	blstypes "github.com/productscience/inference/x/bls/types"
)

const beaconLogModule = "beacon"

const (
	// beaconExtensionBytesPerSlot bounds one slot's share of a beacon vote
	// extension: a 48-byte compressed G1 partial, its slot index varint and
	// protobuf framing.
	beaconExtensionBytesPerSlot = 64
	// beaconExtensionOverheadBytes covers the extension's fixed fields
	// (version, round, key epoch, field tags).
	beaconExtensionOverheadBytes = 512
	// beaconExtensionMinCapBytes is a floor so a misconfigured slot count
	// never makes legitimate extensions oversized.
	beaconExtensionMinCapBytes = 4096
)

// beaconExtensionMaxBytes is the largest vote extension accepted: enough for a
// validator that owns every BLS slot, nothing more. It bounds what a peer can
// make every node store in the extended commit.
func beaconExtensionMaxBytes(totalSlots uint32) int {
	if totalSlots == 0 {
		totalSlots = blstypes.DefaultParams().ITotalSlots
	}
	n := int(totalSlots)*beaconExtensionBytesPerSlot + beaconExtensionOverheadBytes
	if n < beaconExtensionMinCapBytes {
		return beaconExtensionMinCapBytes
	}
	return n
}

// extensionSizeCap returns the vote-extension size cap for the committed state in ctx.
type extensionSizeCap func(ctx sdk.Context) int

// extensionBuilder produces the local vote extension for a height. Step 1 has
// none (empty extension); Step 3 plugs in BLS partial signing.
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

	// Step 2: load the dAPI-exported BLS shares. ExtendVote only triggers their
	// one-time verification; extensions stay empty until Step 3.
	app.setupBeaconShares(appOpts, logger)
	app.SetExtendVoteHandler(newBeaconExtendVoteHandler(logger, beaconShareProbe(
		func() beacon.ShareProvider { return app.BeaconShares },
		app.BlsKeeper.GetCurrentSigningEpochID,
	)))
	sizeCap := func(ctx sdk.Context) int {
		params, err := app.BlsKeeper.GetParams(ctx)
		if err != nil {
			return beaconExtensionMaxBytes(0)
		}
		return beaconExtensionMaxBytes(params.ITotalSlots)
	}
	app.SetVerifyVoteExtensionHandler(newBeaconVerifyVoteExtensionHandler(logger, sizeCap))
	app.SetPrepareProposal(newBeaconPrepareProposalHandler(logger, passThrough))
	app.SetProcessProposal(newBeaconProcessProposalHandler(logger))
	// app.ModuleManager is resolved at call time; runtime sets the PreBlocker
	// order during Load, which is fine because the closure runs later.
	app.SetPreBlocker(newBeaconPreBlocker(moduleManagerPreBlock{app}))
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
func newBeaconVerifyVoteExtensionHandler(logger log.Logger, sizeCap extensionSizeCap) sdk.VerifyVoteExtensionHandler {
	return func(ctx sdk.Context, req *abci.RequestVerifyVoteExtension) (resp *abci.ResponseVerifyVoteExtension, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic in beacon VerifyVoteExtension; rejecting", "height", req.Height, "panic", fmt.Sprint(r))
				resp, err = &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
			}
		}()

		n := len(req.VoteExtension)
		if n == 0 {
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
		}
		if limit := sizeCap(ctx); n > limit {
			logger.Info("rejecting oversized vote extension",
				"height", req.Height,
				"validator", fmt.Sprintf("%X", req.ValidatorAddress),
				"len", n,
				"max", limit)
			return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
		}
		return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
	}
}

// newBeaconPrepareProposalHandler delegates tx selection to inner and logs
// whether the extended commit carries extensions. Step 4 injects EC_h as Txs[0].
func newBeaconPrepareProposalHandler(logger log.Logger, inner sdk.PrepareProposalHandler) sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		withExt, total := countVoteExtensions(req.LocalLastCommit)
		logger.Debug("prepare proposal",
			"height", req.Height,
			"last_commit_votes", total,
			"last_commit_votes_with_extension", withExt)
		return inner(ctx, req)
	}
}

// newBeaconProcessProposalHandler accepts every proposal, matching the SDK's
// NoOpProcessProposal used today. Step 4 adds the injected-EC checks.
func newBeaconProcessProposalHandler(_ log.Logger) sdk.ProcessProposalHandler {
	return func(_ sdk.Context, _ *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
	}
}

// newBeaconPreBlocker runs the module PreBlockers. Step 5 appends beacon
// aggregation from the injected extended commit.
func newBeaconPreBlocker(mm preBlockRunner) sdk.PreBlocker {
	return func(ctx sdk.Context, _ *abci.RequestFinalizeBlock) (*sdk.ResponsePreBlock, error) {
		return mm.PreBlock(ctx)
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

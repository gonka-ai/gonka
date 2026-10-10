package inference

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// Epoch formation reads params, the effective epoch index and the current root
// epoch group data from the store once for the whole EndBlock.
func TestEndBlockEpochFormationReadsParamsOnce(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	fixture.addFreshPoC(t, 100)
	params, err := fixture.keeper.GetParams(fixture.ctx)
	require.NoError(t, err)
	ec, err := types.NewEpochContextFromEffectiveEpoch(fixture.currentEpoch, *params.EpochParams, 0)
	require.NoError(t, err)
	height := ec.EndOfPoCValidation()
	require.True(t, ec.IsEndOfPoCValidationStage(height))
	blockCtx := fixture.ctx.WithBlockHeight(height)

	var trace bytes.Buffer
	blockCtx.MultiStore().SetTracer(&trace)
	require.NoError(t, fixture.module.EndBlock(blockCtx))
	blockCtx.MultiStore().SetTracer(nil)

	stored, found := fixture.keeper.GetActiveParticipants(blockCtx, fixture.upcomingEpoch.Index)
	require.True(t, found, "the epoch was formed")
	require.NotEmpty(t, stored.Participants)

	reads := func(key []byte) int {
		enc := base64.StdEncoding.EncodeToString(key)
		n := 0
		for _, line := range strings.Split(trace.String(), "\n") {
			if strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+enc+`"`) {
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, reads(types.ParamsKey))
	require.Equal(t, 1, reads(types.EffectiveEpochIndexPrefix))
	m := fixture.keeper.EpochGroupDataMap
	rootKey, err := collections.EncodeKeyWithPrefix(m.GetPrefix(), m.KeyCodec(), collections.Join(fixture.currentEpoch.Index, ""))
	require.NoError(t, err)
	require.Equal(t, 1, reads(rootKey), "current root epoch group data")
}

// Switching to the new epoch reads each participant it activates once.
func TestEndBlockSetNewValidatorsReadsParticipantsOnce(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	fixture.addFreshPoC(t, 100)
	params, err := fixture.keeper.GetParams(fixture.ctx)
	require.NoError(t, err)
	ec, err := types.NewEpochContextFromEffectiveEpoch(fixture.currentEpoch, *params.EpochParams, 0)
	require.NoError(t, err)
	require.NoError(t, fixture.module.EndBlock(fixture.ctx.WithBlockHeight(ec.EndOfPoCValidation())))
	active, found := fixture.keeper.GetActiveParticipants(fixture.ctx, fixture.upcomingEpoch.Index)
	require.True(t, found)
	require.NotEmpty(t, active.Participants)

	blockCtx := fixture.ctx.WithBlockHeight(ec.SetNewValidators())
	var trace bytes.Buffer
	blockCtx.MultiStore().SetTracer(&trace)
	require.NoError(t, fixture.module.EndBlock(blockCtx))
	blockCtx.MultiStore().SetTracer(nil)

	m := fixture.keeper.Participants
	for _, p := range active.Participants {
		addr, err := sdk.AccAddressFromBech32(p.Index)
		require.NoError(t, err)
		key, err := collections.EncodeKeyWithPrefix(m.GetPrefix(), m.KeyCodec(), addr)
		require.NoError(t, err)
		enc := base64.StdEncoding.EncodeToString(key)
		reads := strings.Count(trace.String(), `"operation":"read","key":"`+enc+`"`)
		require.Equal(t, 1, reads, p.Index)
		stored, found := fixture.keeper.GetParticipant(blockCtx, p.Index)
		require.True(t, found)
		require.Equal(t, types.ParticipantStatus_ACTIVE, stored.Status)
	}
}

// Reading each participant once keeps the status recompute a fresh read made:
// a stored confirmation ratio below alpha still deactivates at the switch.
func TestEndBlockSetNewValidatorsRecomputesStoredRatio(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	fixture.addFreshPoC(t, 100)
	params, err := fixture.keeper.GetParams(fixture.ctx)
	require.NoError(t, err)
	params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.7)
	require.NoError(t, fixture.keeper.SetParams(fixture.ctx, params))
	ec, err := types.NewEpochContextFromEffectiveEpoch(fixture.currentEpoch, *params.EpochParams, 0)
	require.NoError(t, err)
	require.NoError(t, fixture.module.EndBlock(fixture.ctx.WithBlockHeight(ec.EndOfPoCValidation())))
	active, found := fixture.keeper.GetActiveParticipants(fixture.ctx, fixture.upcomingEpoch.Index)
	require.True(t, found)
	require.NotEmpty(t, active.Participants)
	for _, ap := range active.Participants {
		p, ok := fixture.keeper.GetParticipant(fixture.ctx, ap.Index)
		require.True(t, ok)
		if p.CurrentEpochStats == nil {
			p.CurrentEpochStats = types.NewCurrentEpochStats()
		}
		p.CurrentEpochStats.ConfirmationPoCRatio = types.DecimalFromFloat(0.1)
		require.NoError(t, fixture.keeper.Participants.Set(fixture.ctx, sdk.MustAccAddressFromBech32(ap.Index), p))
	}

	blockCtx := fixture.ctx.WithBlockHeight(ec.SetNewValidators())
	require.NoError(t, fixture.module.EndBlock(blockCtx))
	for _, ap := range active.Participants {
		p, found := fixture.keeper.GetParticipant(blockCtx, ap.Index)
		require.True(t, found)
		require.Equal(t, types.ParticipantStatus_INACTIVE, p.Status, ap.Index)
	}
}

// The validation-start block builds the delegation and validation snapshots from
// one read of the base state and one pass over the stage's store commits.
func TestEndBlockValidationStartReadsStageInputsOnce(t *testing.T) {
	fixture := newFormationRecoveryFixture(t, noopCollateralKeeper{}, 1, 2)
	fixture.addFreshPoC(t, 100)
	params, err := fixture.keeper.GetParams(fixture.ctx)
	require.NoError(t, err)
	ec, err := types.NewEpochContextFromEffectiveEpoch(fixture.currentEpoch, *params.EpochParams, 0)
	require.NoError(t, err)
	height := ec.StartOfPoCValidation()
	require.True(t, ec.IsStartOfPoCValidationStage(height))
	blockCtx := fixture.ctx.WithBlockHeight(height)

	commitPrefix := fixture.keeper.PoCV2StoreCommits.GetPrefix()
	traced := func(run func()) (apReads, commitIters int) {
		var trace bytes.Buffer
		blockCtx.MultiStore().SetTracer(&trace)
		run()
		blockCtx.MultiStore().SetTracer(nil)
		apKey := base64.StdEncoding.EncodeToString(types.ActiveParticipantsFullKey(fixture.currentEpoch.Index))
		for _, line := range strings.Split(trace.String(), "\n") {
			if strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+apKey+`"`) {
				apReads++
			}
			if !strings.Contains(line, `"operation":"iterKey"`) {
				continue
			}
			var op struct {
				Key []byte `json:"key"`
			}
			require.NoError(t, json.Unmarshal([]byte(line), &op))
			if bytes.HasPrefix(op.Key, commitPrefix) {
				commitIters++
			}
		}
		return apReads, commitIters
	}

	_, onePass := traced(func() {
		_, err := fixture.keeper.GetAllPoCV2StoreCommitsForStage(blockCtx, fixture.upcomingEpoch.PocStartBlockHeight)
		require.NoError(t, err)
	})
	require.Positive(t, onePass)
	apReads, commitIters := traced(func() { require.NoError(t, fixture.module.EndBlock(blockCtx)) })

	_, found := fixture.keeper.GetDelegationSnapshot(blockCtx)
	require.True(t, found, "delegation snapshot written")
	snapshot, found, err := fixture.keeper.GetPoCValidationSnapshot(blockCtx, fixture.upcomingEpoch.PocStartBlockHeight)
	require.NoError(t, err)
	require.True(t, found, "validation snapshot written")
	require.Equal(t, height, snapshot.SnapshotHeight)
	require.Equal(t, 1, apReads, "active participants of the effective epoch")
	require.Equal(t, onePass, commitIters, "one pass over the stage's store commits")
}

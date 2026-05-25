package keeper_test

import (
	"crypto/sha256"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func makeTestPublicKey(b byte) []byte {
	pk := make([]byte, 32)
	for i := range pk {
		pk[i] = b
	}
	return pk
}

func makeQuoteHash(quote []byte) []byte {
	sum := sha256.Sum256(quote)
	return append([]byte(nil), sum[:]...)
}

func TestTeeAttestation_SetGetRemove(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	addr, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)

	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x01),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    140,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	got, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "intel-tdx-lite", got.Provider)
	require.Equal(t, int64(140), got.ExpiresAtHeight)
	require.Equal(t, makeTestPublicKey(0x01), got.PublicKey)

	require.NoError(t, k.RemoveTeeAttestation(sdkCtx, addr, "node-1"))
	_, found, err = k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.False(t, found)
}

func TestTeeAttestation_SetReplacesAndUpdatesIndex(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x01),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    140,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	updated := att
	updated.PublicKey = makeTestPublicKey(0x02)
	updated.ExpiresAtHeight = 180
	require.NoError(t, k.SetTeeAttestation(sdkCtx, updated))

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	got, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, makeTestPublicKey(0x02), got.PublicKey)
	require.Equal(t, int64(180), got.ExpiresAtHeight)

	pruned, err := k.PruneExpiredTeeAttestations(sdkCtx, 140, 64)
	require.NoError(t, err)
	require.Equal(t, 0, pruned)
	_, found, _ = k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.True(t, found)
}

func TestTeeAttestation_PendingStageIndexTracksStatusTransitions(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)
	addr, err := sdk.AccAddressFromBech32(testutil.Executor)
	require.NoError(t, err)

	att := types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x01),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    140,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	stage100Key := collections.Join3(int64(100), addr, "node-1")
	has, err := k.TeePendingAttestationsByStage.Has(sdkCtx, stage100Key)
	require.NoError(t, err)
	require.True(t, has)

	att.VerificationStatus = types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	has, err = k.TeePendingAttestationsByStage.Has(sdkCtx, stage100Key)
	require.NoError(t, err)
	require.False(t, has)

	att.VerificationStatus = types.TeeVerificationStatus_TEE_VERIFICATION_PENDING
	att.AttestedAtHeight = 120
	att.ExpiresAtHeight = 180
	require.NoError(t, k.SetTeeAttestation(sdkCtx, att))

	stage120Key := collections.Join3(int64(120), addr, "node-1")
	has, err = k.TeePendingAttestationsByStage.Has(sdkCtx, stage120Key)
	require.NoError(t, err)
	require.True(t, has)
}

func TestTeeAttestation_ListByParticipant(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)
	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	other, _ := sdk.AccAddressFromBech32(testutil.Executor2)
	_ = other

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		att := types.TeeAttestation{
			ParticipantAddress: testutil.Executor,
			NodeId:             nodeID,
			PublicKey:          makeTestPublicKey(0x01),
			Provider:           "intel-tdx-lite",
			EnvelopeVersion:    "v1",
			AttestedAtHeight:   100,
			ExpiresAtHeight:    140,
		}
		require.NoError(t, k.SetTeeAttestation(sdkCtx, att))
	}

	otherAtt := types.TeeAttestation{
		ParticipantAddress: testutil.Executor2,
		NodeId:             "node-X",
		PublicKey:          makeTestPublicKey(0x02),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    "v1",
		AttestedAtHeight:   100,
		ExpiresAtHeight:    140,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, otherAtt))

	atts, err := k.ListTeeAttestationsByParticipant(sdkCtx, addr)
	require.NoError(t, err)
	require.Len(t, atts, 3)
}

func TestTeeAttestation_Pruning(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)

	atts := []types.TeeAttestation{
		{ParticipantAddress: testutil.Executor, NodeId: "expires-100", PublicKey: makeTestPublicKey(0x01), Provider: "intel-tdx-lite", EnvelopeVersion: "v1", AttestedAtHeight: 60, ExpiresAtHeight: 100},
		{ParticipantAddress: testutil.Executor, NodeId: "expires-150", PublicKey: makeTestPublicKey(0x02), Provider: "intel-tdx-lite", EnvelopeVersion: "v1", AttestedAtHeight: 110, ExpiresAtHeight: 150},
		{ParticipantAddress: testutil.Executor, NodeId: "expires-300", PublicKey: makeTestPublicKey(0x03), Provider: "intel-tdx-lite", EnvelopeVersion: "v1", AttestedAtHeight: 260, ExpiresAtHeight: 300},
	}
	for _, a := range atts {
		require.NoError(t, k.SetTeeAttestation(sdkCtx, a))
	}

	pruned, err := k.PruneExpiredTeeAttestations(sdkCtx, 200, 32)
	require.NoError(t, err)
	require.Equal(t, 2, pruned)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	remaining, err := k.ListTeeAttestationsByParticipant(sdkCtx, addr)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, "expires-300", remaining[0].NodeId)
}

func TestTeeAttestation_PruningRespectsBudget(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(500)

	for i := 0; i < 10; i++ {
		att := types.TeeAttestation{
			ParticipantAddress: testutil.Executor,
			NodeId:             string(rune('a'+i)) + "-node",
			PublicKey:          makeTestPublicKey(byte(i)),
			Provider:           "intel-tdx-lite",
			EnvelopeVersion:    "v1",
			AttestedAtHeight:   100,
			ExpiresAtHeight:    100 + int64(i),
		}
		require.NoError(t, k.SetTeeAttestation(sdkCtx, att))
	}

	pruned, err := k.PruneExpiredTeeAttestations(sdkCtx, 500, 4)
	require.NoError(t, err)
	require.Equal(t, 4, pruned)

	pruned, err = k.PruneExpiredTeeAttestations(sdkCtx, 500, 100)
	require.NoError(t, err)
	require.Equal(t, 6, pruned)
}

func TestSubmitTeeAttestation_HappyPath(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)

	requireParticipant(t, k, sdkCtx, testutil.Executor)

	msgServer := keeper.NewMsgServerImpl(k)
	msg := &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations: []*types.TeeAttestationEntry{
			validTeeAttestationEntry("node-1", 0x01),
		},
	}
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, msg)
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(100), att.AttestedAtHeight)

	require.Equal(t, int64(140), att.ExpiresAtHeight)
	require.Equal(t, "v1", att.EnvelopeVersion)
}

func TestSubmitTeeAttestation_PersistsQuoteBytes(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	msgServer := keeper.NewMsgServerImpl(k)
	entry := validTeeAttestationEntry("node-1", 0x01)
	msg := &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{entry},
	}
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, msg)
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, entry.Quote, att.Quote)
	require.Equal(t, entry.QuoteHash, att.QuoteHash)
}

func TestSubmitTeeAttestation_RefreshPreservesVerificationStateForSameIdentity(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	entry := validTeeAttestationEntry("node-1", 0x11)
	previous := types.TeeAttestation{
		ParticipantAddress:   testutil.Executor,
		NodeId:               entry.NodeId,
		PublicKey:            entry.PublicKey,
		Provider:             entry.Provider,
		EnvelopeVersion:      entry.EnvelopeVersion,
		Measurement:          entry.Measurement,
		AttestedAtHeight:     60,
		ExpiresAtHeight:      100,
		VerificationStatus:   types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
		VerifiedAtHeight:     77,
		OkVotingPowerBps:     7300,
		RejectVotingPowerBps: 900,
		PendingEpochs:        2,
		Quote:                entry.Quote,
		QuoteHash:            entry.QuoteHash,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, previous))

	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{entry},
	})
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED, att.VerificationStatus)
	require.Equal(t, int64(77), att.VerifiedAtHeight)
	require.Equal(t, uint32(7300), att.OkVotingPowerBps)
	require.Equal(t, uint32(900), att.RejectVotingPowerBps)
	require.Equal(t, uint32(2), att.PendingEpochs)
	require.Equal(t, int64(100), att.AttestedAtHeight)
	require.Equal(t, int64(140), att.ExpiresAtHeight)
	require.Equal(t, entry.Quote, att.Quote)
	require.Equal(t, entry.QuoteHash, att.QuoteHash)
}

func TestSubmitTeeAttestation_RefreshResetsVerificationStateWhenQuoteHashChanges(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	oldQuote := []byte("old-quote")
	previous := types.TeeAttestation{
		ParticipantAddress:   testutil.Executor,
		NodeId:               "node-1",
		PublicKey:            makeTestPublicKey(0x11),
		Provider:             "intel-tdx-lite",
		EnvelopeVersion:      "v1",
		Measurement:          testTeeMeasurementSeeded,
		AttestedAtHeight:     60,
		ExpiresAtHeight:      100,
		VerificationStatus:   types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
		VerifiedAtHeight:     77,
		OkVotingPowerBps:     7300,
		RejectVotingPowerBps: 900,
		PendingEpochs:        2,
		Quote:                oldQuote,
		QuoteHash:            makeQuoteHash(oldQuote),
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, previous))

	newQuote := []byte("new-quote-bytes-for-node-1")
	newEntry := validTeeAttestationEntry("node-1", 0x11)
	newEntry.Quote = newQuote
	newEntry.QuoteHash = makeQuoteHash(newQuote)
	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{newEntry},
	})
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_PENDING, att.VerificationStatus)
	require.Equal(t, int64(0), att.VerifiedAtHeight)
	require.Equal(t, uint32(0), att.OkVotingPowerBps)
	require.Equal(t, uint32(0), att.RejectVotingPowerBps)
	require.Equal(t, uint32(0), att.PendingEpochs)
}

func TestSubmitTeeAttestation_RefreshResetsVerificationStateWhenIdentityChanges(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	previous := types.TeeAttestation{
		ParticipantAddress:   testutil.Executor,
		NodeId:               "node-1",
		PublicKey:            makeTestPublicKey(0x01),
		Provider:             "intel-tdx-lite",
		EnvelopeVersion:      "v1",
		AttestedAtHeight:     60,
		ExpiresAtHeight:      100,
		VerificationStatus:   types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
		VerifiedAtHeight:     77,
		OkVotingPowerBps:     7000,
		RejectVotingPowerBps: 500,
		PendingEpochs:        3,
	}
	require.NoError(t, k.SetTeeAttestation(sdkCtx, previous))

	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{validTeeAttestationEntry("node-1", 0x02)},
	})
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.TeeVerificationStatus_TEE_VERIFICATION_PENDING, att.VerificationStatus)
	require.Equal(t, int64(0), att.VerifiedAtHeight)
	require.Equal(t, uint32(0), att.OkVotingPowerBps)
	require.Equal(t, uint32(0), att.RejectVotingPowerBps)
	require.Equal(t, uint32(0), att.PendingEpochs)
}

func TestSubmitTeeAttestation_RejectsBadProvider(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	entry := validTeeAttestationEntry("node-1", 0x01)
	entry.Provider = "snake-oil-tee"
	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{entry},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationProviderNotAllowed)
}

func TestSubmitTeeAttestation_RejectsBadPublicKey(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	entry := validTeeAttestationEntry("node-1", 0x01)
	entry.PublicKey = []byte{0x01, 0x02}
	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{entry},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationInvalidPublicKey)
}

func TestSubmitTeeAttestation_RejectsOutsidePocWindow(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)
	sdkCtx = sdkCtx.WithBlockHeight(180)

	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{validTeeAttestationEntry("node-1", 0x01)},
	})
	require.ErrorIs(t, err, types.ErrPocTooLate)
}

func TestSubmitTeeAttestation_AlwaysRequiresQuote(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	msgServer := keeper.NewMsgServerImpl(k)

	missingQuote := validTeeAttestationEntry("node-1", 0x01)
	missingQuote.Quote = nil
	missingQuote.QuoteHash = nil
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{missingQuote},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationQuoteRequired)

	emptyQuote := validTeeAttestationEntry("node-1", 0x01)
	empty := sha256.Sum256(nil)
	emptyQuote.Quote = nil
	emptyQuote.QuoteHash = empty[:]
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{emptyQuote},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationQuoteRequired)

	badHashLen := validTeeAttestationEntry("node-1", 0x01)
	badHashLen.QuoteHash = []byte{0x01, 0x02}
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{badHashLen},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationQuoteRequired)

	mismatched := validTeeAttestationEntry("node-1", 0x01)
	mismatched.QuoteHash = make([]byte, sha256.Size)
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{mismatched},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationQuoteRequired)

	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{validTeeAttestationEntry("node-1", 0x01)},
	})
	require.NoError(t, err)
}

func TestSubmitTeeAttestation_DisabledWhenAllowedMeasurementsEmpty(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.TeeAttestationParams.AllowedMeasurements = nil
	require.NoError(t, k.SetParams(sdkCtx, params))

	msgServer := keeper.NewMsgServerImpl(k)
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{validTeeAttestationEntry("node-1", 0x01)},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationDisabled)
}

func TestSubmitTeeAttestation_ProviderCanonicalized(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.TeeAttestationParams.AllowedProviders = []string{"Intel-TDX"}
	require.NoError(t, k.SetParams(sdkCtx, params))

	entry := validTeeAttestationEntry("node-1", 0x01)
	entry.Provider = "INTEL-TDX"
	msgServer := keeper.NewMsgServerImpl(k)
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{entry},
	})
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "intel-tdx", att.Provider)
}

func TestSubmitTeeAttestation_EnforcesMaxEntriesPerMessage(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.TeeAttestationParams.MaxEntriesPerMessage = 2
	require.NoError(t, k.SetParams(sdkCtx, params))

	msgServer := keeper.NewMsgServerImpl(k)
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations: []*types.TeeAttestationEntry{
			validTeeAttestationEntry("node-1", 0x01),
			validTeeAttestationEntry("node-2", 0x02),
			validTeeAttestationEntry("node-3", 0x03),
		},
	})
	require.ErrorIs(t, err, types.ErrIllegalState)
}

func TestSubmitTeeAttestation_RejectsDuplicateNodeIdsInMessage(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations: []*types.TeeAttestationEntry{
			validTeeAttestationEntry("node-1", 0x01),
			validTeeAttestationEntry("node-1", 0x02),
		},
	})
	require.ErrorIs(t, err, types.ErrDuplicateNodeId)
}

func TestSubmitTeeAttestation_EnforcesMeasurementAllowlist(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.TeeAttestationParams.AllowedMeasurements = []string{"deadbeef"}
	require.NoError(t, k.SetParams(sdkCtx, params))

	msgServer := keeper.NewMsgServerImpl(k)

	missing := validTeeAttestationEntry("node-1", 0x01)
	missing.Measurement = ""
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{missing},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationMeasurementNotAllowed)

	wrong := validTeeAttestationEntry("node-1", 0x01)
	wrong.Measurement = "0badcafe"
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{wrong},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationMeasurementNotAllowed)

	accepted := validTeeAttestationEntry("node-1", 0x01)
	accepted.Measurement = "0xDEADBEEF"
	_, err = msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{accepted},
	})
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	att, found, err := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "deadbeef", att.Measurement)
}

func TestSubmitTeeAttestation_RejectsMalformedMeasurement(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	entry := validTeeAttestationEntry("node-1", 0x01)
	entry.Measurement = "not-hex"
	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{entry},
	})
	require.ErrorIs(t, err, types.ErrTeeAttestationMeasurementNotAllowed)
}

func TestSubmitTeeAttestation_EmitsEvent(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	msgServer := keeper.NewMsgServerImpl(k)
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations:             []*types.TeeAttestationEntry{validTeeAttestationEntry("node-1", 0x01)},
	})
	require.NoError(t, err)

	events := sdkCtx.EventManager().Events()
	found := false
	for _, ev := range events {
		if ev.Type != keeper.EventTypeTeeAttestationSubmitted {
			continue
		}
		found = true
		var participant, nodeID string
		for _, a := range ev.Attributes {
			switch a.Key {
			case keeper.AttributeKeyTeeParticipant:
				participant = a.Value
			case keeper.AttributeKeyTeeNodeID:
				nodeID = a.Value
			}
		}
		require.Equal(t, testutil.Executor, participant)
		require.Equal(t, "node-1", nodeID)
	}
	require.True(t, found, "tee_attestation_submitted event not emitted")
}

func TestRevokeTeeAttestation_RemovesEntries(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)
	requireParticipant(t, k, sdkCtx, testutil.Executor)

	msgServer := keeper.NewMsgServerImpl(k)
	submit := &types.MsgSubmitTeeAttestation{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Attestations: []*types.TeeAttestationEntry{
			validTeeAttestationEntry("node-1", 0x01),
			validTeeAttestationEntry("node-2", 0x02),
		},
	}
	_, err := msgServer.SubmitTeeAttestation(sdkCtx, submit)
	require.NoError(t, err)

	revoke := &types.MsgRevokeTeeAttestation{
		Creator: testutil.Executor,
		NodeIds: []string{"node-1"},
	}
	_, err = msgServer.RevokeTeeAttestation(sdkCtx, revoke)
	require.NoError(t, err)

	addr, _ := sdk.AccAddressFromBech32(testutil.Executor)
	_, found, _ := k.GetTeeAttestation(sdkCtx, addr, "node-1")
	require.False(t, found)
	_, found, _ = k.GetTeeAttestation(sdkCtx, addr, "node-2")
	require.True(t, found)
}

const (
	testTeeQuotePrefix       = "quote-bytes-for-"
	testTeeMeasurementSeeded = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
)

func testTeeQuoteFor(nodeID string) []byte {
	return []byte(testTeeQuotePrefix + nodeID)
}

func validTeeAttestationEntry(nodeID string, pkByte byte) *types.TeeAttestationEntry {
	quote := testTeeQuoteFor(nodeID)
	return &types.TeeAttestationEntry{
		NodeId:          nodeID,
		PublicKey:       makeTestPublicKey(pkByte),
		Provider:        "intel-tdx-lite",
		EnvelopeVersion: "v1",
		Quote:           quote,
		QuoteHash:       makeQuoteHash(quote),
		Measurement:     testTeeMeasurementSeeded,
	}
}

func setupTeeTestContext(t *testing.T, k keeper.Keeper, ctx sdk.Context) sdk.Context {
	t.Helper()
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.EpochParams = &types.EpochParams{
		EpochLength:           40,
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	params.TeeAttestationParams = types.DefaultTeeAttestationParams()
	params.TeeAttestationParams.AllowedProviders = append(params.TeeAttestationParams.AllowedProviders, "intel-tdx-lite")
	params.TeeAttestationParams.AllowedMeasurements = []string{testTeeMeasurementSeeded}
	require.NoError(t, k.SetParams(sdkCtx, params))

	k.SetEffectiveEpochIndex(sdkCtx, 0)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
	return sdkCtx
}

func requireParticipant(t *testing.T, k keeper.Keeper, ctx sdk.Context, address string) {
	t.Helper()
	addr, err := sdk.AccAddressFromBech32(address)
	require.NoError(t, err)

	require.NoError(t, k.Participants.Set(ctx, addr, types.Participant{Address: address, Index: addr.String()}))
}

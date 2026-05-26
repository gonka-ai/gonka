package keeper_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
)

func TestTeeAttestationsVerified_EmptyWhenOnlyPending(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)

	require.NoError(t, k.SetTeeAttestation(sdkCtx, types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-1",
		PublicKey:          makeTestPublicKey(0x01),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    types.TeeEnvelopeVersionV1,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}))

	resp, err := k.TeeAttestationsVerified(sdkCtx, &types.QueryTeeAttestationsVerifiedRequest{})
	require.NoError(t, err)
	require.Empty(t, resp.Attestations)
}

func TestTeeAttestationsVerified_ReturnsOnlyVerified(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)

	subjVerified := sample.AccAddress()
	require.NoError(t, k.SetTeeAttestation(sdkCtx, types.TeeAttestation{
		ParticipantAddress: subjVerified,
		NodeId:             "node-verified",
		PublicKey:          makeTestPublicKey(0x02),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    types.TeeEnvelopeVersionV1,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
	}))

	subjPending := sample.AccAddress()
	require.NoError(t, k.SetTeeAttestation(sdkCtx, types.TeeAttestation{
		ParticipantAddress: subjPending,
		NodeId:             "node-pending",
		PublicKey:          makeTestPublicKey(0x03),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    types.TeeEnvelopeVersionV1,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_PENDING,
	}))

	resp, err := k.TeeAttestationsVerified(sdkCtx, &types.QueryTeeAttestationsVerifiedRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Attestations, 1)
	require.Equal(t, "node-verified", resp.Attestations[0].NodeId)
}

func TestTeeAttestationsVerified_EmptyWhenOnlyRejected(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)

	require.NoError(t, k.SetTeeAttestation(sdkCtx, types.TeeAttestation{
		ParticipantAddress: testutil.Executor,
		NodeId:             "node-rejected",
		PublicKey:          makeTestPublicKey(0x04),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    types.TeeEnvelopeVersionV1,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_REJECTED,
	}))

	resp, err := k.TeeAttestationsVerified(sdkCtx, &types.QueryTeeAttestationsVerifiedRequest{})
	require.NoError(t, err)
	require.Empty(t, resp.Attestations)
}

func TestTeeAttestationsVerified_PaginatesAcrossPages(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := setupTeeTestContext(t, k, ctx)

	subjA := sample.AccAddress()
	require.NoError(t, k.SetTeeAttestation(sdkCtx, types.TeeAttestation{
		ParticipantAddress: subjA,
		NodeId:             "node-a",
		PublicKey:          makeTestPublicKey(0x05),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    types.TeeEnvelopeVersionV1,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
	}))
	subjB := sample.AccAddress()
	require.NoError(t, k.SetTeeAttestation(sdkCtx, types.TeeAttestation{
		ParticipantAddress: subjB,
		NodeId:             "node-b",
		PublicKey:          makeTestPublicKey(0x06),
		Provider:           "intel-tdx-lite",
		EnvelopeVersion:    types.TeeEnvelopeVersionV1,
		AttestedAtHeight:   100,
		ExpiresAtHeight:    1000,
		VerificationStatus: types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED,
	}))

	first, err := k.TeeAttestationsVerified(sdkCtx, &types.QueryTeeAttestationsVerifiedRequest{
		Pagination: &query.PageRequest{Limit: 1},
	})
	require.NoError(t, err)
	require.Len(t, first.Attestations, 1)
	require.NotNil(t, first.Pagination)
	require.NotEmpty(t, first.Pagination.NextKey)

	second, err := k.TeeAttestationsVerified(sdkCtx, &types.QueryTeeAttestationsVerifiedRequest{
		Pagination: &query.PageRequest{
			Limit: 1,
			Key:   first.Pagination.NextKey,
		},
	})
	require.NoError(t, err)
	require.Len(t, second.Attestations, 1)
	require.NotEqual(t, first.Attestations[0].NodeId, second.Attestations[0].NodeId)
}

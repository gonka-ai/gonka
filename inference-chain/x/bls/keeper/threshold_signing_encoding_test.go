package keeper_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestRequestThresholdSignature_EncodingIncludesAttemptField(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(901)
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	signingData := types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{0x11}, 32),
		RequestId:      bytes.Repeat([]byte{0x22}, 32),
		Data: [][]byte{
			bytes.Repeat([]byte{0x33}, 32),
			bytes.Repeat([]byte{0x44}, 20),
			bytes.Repeat([]byte{0x55}, 20),
			bytes.Repeat([]byte{0x66}, 32),
		},
	}

	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.EqualValues(t, 1, request.Attempt)

	expectedEncoded := buildExpectedEncodedData(signingData, request.Attempt)
	require.Equal(t, expectedEncoded, request.EncodedData)

	attemptOffset := 8 + 32 + 32
	require.Equal(t, []byte{0x00, 0x00, 0x00, 0x01}, request.EncodedData[attemptOffset:attemptOffset+4])

	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(expectedEncoded)
	expectedHash := hasher.Sum(nil)
	require.Equal(t, expectedHash, request.MessageHash)
}

func TestProcessThresholdSigningDeadlines_RetryReencodesWithIncrementedAttempt(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(902)
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	signingData := types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{0x71}, 32),
		RequestId:      bytes.Repeat([]byte{0x72}, 32),
		Data: [][]byte{
			bytes.Repeat([]byte{0x73}, 32),
			bytes.Repeat([]byte{0x74}, 20),
			bytes.Repeat([]byte{0x75}, 32),
		},
	}

	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))

	initialRequest, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.EqualValues(t, 1, initialRequest.Attempt)

	retryCtx := ctx.WithBlockHeight(initialRequest.DeadlineBlockHeight)
	require.NoError(t, k.ProcessThresholdSigningDeadlines(retryCtx))

	retryRequest, err := k.GetSigningStatus(retryCtx, signingData.RequestId)
	require.NoError(t, err)
	require.EqualValues(t, 2, retryRequest.Attempt)

	expectedEncoded := buildExpectedEncodedData(signingData, retryRequest.Attempt)
	require.Equal(t, expectedEncoded, retryRequest.EncodedData)

	attemptOffset := 8 + 32 + 32
	require.Equal(t, []byte{0x00, 0x00, 0x00, 0x02}, retryRequest.EncodedData[attemptOffset:attemptOffset+4])

	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(expectedEncoded)
	expectedHash := hasher.Sum(nil)
	require.Equal(t, expectedHash, retryRequest.MessageHash)
}

func buildExpectedEncodedData(signingData types.SigningData, attempt uint32) []byte {
	encoded := make([]byte, 0, 8+len(signingData.ChainId)+len(signingData.RequestId)+4)

	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, signingData.CurrentEpochId)
	encoded = append(encoded, epochBytes...)
	encoded = append(encoded, signingData.ChainId...)
	encoded = append(encoded, signingData.RequestId...)

	attemptBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(attemptBytes, attempt)
	encoded = append(encoded, attemptBytes...)

	for _, element := range signingData.Data {
		encoded = append(encoded, element...)
	}

	return encoded
}

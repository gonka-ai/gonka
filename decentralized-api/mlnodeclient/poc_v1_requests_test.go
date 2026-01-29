package mlnodeclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildInitDtoV1(t *testing.T) {
	dto := BuildInitDtoV1(
		100,           // blockHeight
		"testpubkey",  // pubKey
		5,             // totalNodes
		2,             // nodeNum
		"blockhash",   // blockHash
		"http://callback", // callbackUrl
		nil,           // chainModelParams (nil uses defaults)
	)

	assert.Equal(t, int64(100), dto.BlockHeight)
	assert.Equal(t, "testpubkey", dto.PublicKey)
	assert.Equal(t, int64(5), dto.TotalNodes)
	assert.Equal(t, uint64(2), dto.NodeNum)
	assert.Equal(t, "blockhash", dto.BlockHash)
	assert.Equal(t, "http://callback", dto.URL)
	assert.Equal(t, DefaultBatchSize, dto.BatchSize)
	assert.Equal(t, DefaultRTarget, dto.RTarget)
	assert.Equal(t, DefaultFraudThreshold, dto.FraudThreshold)
	require.NotNil(t, dto.Params)
}

func TestParamsV1Defaults(t *testing.T) {
	// TestNet params
	assert.Equal(t, 1024, TestNetParamsV1.Dim)
	assert.Equal(t, 32, TestNetParamsV1.NLayers)
	assert.Equal(t, 128, TestNetParamsV1.SeqLen)

	// MainNet params
	assert.Equal(t, 1792, MainNetParamsV1.Dim)
	assert.Equal(t, 64, MainNetParamsV1.NLayers)
	assert.Equal(t, 256, MainNetParamsV1.SeqLen)
}

func TestPowStateV1Constants(t *testing.T) {
	// Verify V1 status constants are correct
	assert.Equal(t, PowStateV1("IDLE"), PowStateV1Idle)
	assert.Equal(t, PowStateV1("GENERATING"), PowStateV1Generating)
	assert.Equal(t, PowStateV1("VALIDATING"), PowStateV1Validating)
	assert.Equal(t, PowStateV1("STOPPED"), PowStateV1Stopped)
}

func TestProofBatchV1Structure(t *testing.T) {
	batch := ProofBatchV1{
		PublicKey:   "pubkey123",
		BlockHash:   "hash456",
		BlockHeight: 200,
		Nonces:      []int64{1, 2, 3},
		Dist:        []float64{0.1, 0.2, 0.3},
		NodeNum:     1,
	}

	assert.Equal(t, "pubkey123", batch.PublicKey)
	assert.Equal(t, "hash456", batch.BlockHash)
	assert.Equal(t, int64(200), batch.BlockHeight)
	assert.Len(t, batch.Nonces, 3)
	assert.Len(t, batch.Dist, 3)
	assert.Equal(t, uint64(1), batch.NodeNum)
}

func TestValidatedBatchV1Structure(t *testing.T) {
	batch := ValidatedBatchV1{
		ProofBatchV1: ProofBatchV1{
			PublicKey:   "pubkey123",
			BlockHash:   "hash456",
			BlockHeight: 200,
			Nonces:      []int64{1, 2, 3},
			Dist:        []float64{0.1, 0.2, 0.3},
			NodeNum:     1,
		},
		ReceivedDist:      []float64{0.11, 0.21, 0.31},
		RTarget:           1.5,
		FraudThreshold:    1e-7,
		NInvalid:          0,
		ProbabilityHonest: 0.99,
		FraudDetected:     false,
	}

	assert.Equal(t, "pubkey123", batch.PublicKey)
	assert.Equal(t, int64(200), batch.BlockHeight)
	assert.Len(t, batch.ReceivedDist, 3)
	assert.Equal(t, 1.5, batch.RTarget)
	assert.Equal(t, int64(0), batch.NInvalid)
	assert.False(t, batch.FraudDetected)
}

func TestMockClientV1Methods(t *testing.T) {
	mock := NewMockClient()

	// Test InitGenerateV1
	dto := InitDtoV1{BlockHeight: 100, PublicKey: "pk"}
	err := mock.InitGenerateV1(nil, dto)
	require.NoError(t, err)
	assert.Equal(t, 1, mock.InitGenerateV1Called)
	assert.Equal(t, MlNodeState_POW, mock.CurrentState)
	assert.Equal(t, PowStateV1Generating, mock.PowStatusV1)

	// Test InitValidateV1
	err = mock.InitValidateV1(nil, dto)
	require.NoError(t, err)
	assert.Equal(t, 1, mock.InitValidateV1Called)
	assert.Equal(t, PowStateV1Validating, mock.PowStatusV1)

	// Test ValidateBatchV1
	batch := ProofBatchV1{BlockHeight: 100, Nonces: []int64{1}}
	err = mock.ValidateBatchV1(nil, batch)
	require.NoError(t, err)
	assert.Equal(t, 1, mock.ValidateBatchV1Called)

	// Test GetPowStatusV1
	status, err := mock.GetPowStatusV1(nil)
	require.NoError(t, err)
	assert.Equal(t, 1, mock.GetPowStatusV1Called)
	assert.Equal(t, PowStateV1Validating, status.Status)
}

func TestMockClientV1ErrorInjection(t *testing.T) {
	mock := NewMockClient()
	mock.InitGenerateV1Error = assert.AnError

	dto := InitDtoV1{BlockHeight: 100}
	err := mock.InitGenerateV1(nil, dto)
	require.Error(t, err)
	assert.Equal(t, assert.AnError, err)
}

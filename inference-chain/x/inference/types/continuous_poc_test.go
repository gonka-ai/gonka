package types_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// ── ContinuousPoCParams serialization round-trip ─────────────────────────────

func TestContinuousPoCParams_MarshalRoundTrip(t *testing.T) {
	original := &types.ContinuousPoCParams{
		EnableContinuousPoC:               true,
		PocUtilizationTargetBps:           9950,
		NonceWeight:                       10,
		MaxCommitsPerEpoch:                100,
		MinNoncesPerCommit:                10,
		ValidationSampleRateBps:           500,
		ValidationChallengeDeadlineBlocks: 50,
	}

	bz, err := original.Marshal()
	require.NoError(t, err)

	decoded := &types.ContinuousPoCParams{}
	err = decoded.Unmarshal(bz)
	require.NoError(t, err)
	require.True(t, original.Equal(decoded))
}

func TestContinuousPoCParams_DefaultsRoundTrip(t *testing.T) {
	p := types.DefaultContinuousPoCParams()
	bz, err := p.Marshal()
	require.NoError(t, err)

	decoded := &types.ContinuousPoCParams{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.True(t, p.Equal(decoded))
}

func TestContinuousPoCParams_Validate(t *testing.T) {
	cases := []struct {
		name    string
		params  *types.ContinuousPoCParams
		wantErr bool
	}{
		{"nil params are valid", nil, false},
		{"defaults are valid", types.DefaultContinuousPoCParams(), false},
		{
			"utilization > 10000 is invalid",
			&types.ContinuousPoCParams{PocUtilizationTargetBps: 10001},
			true,
		},
		{
			"enabled with zero nonce_weight is invalid",
			&types.ContinuousPoCParams{
				EnableContinuousPoC:               true,
				NonceWeight:                       0,
				ValidationChallengeDeadlineBlocks: 50,
			},
			true,
		},
		{
			"enabled with zero deadline is invalid",
			&types.ContinuousPoCParams{
				EnableContinuousPoC:               true,
				NonceWeight:                       10,
				ValidationChallengeDeadlineBlocks: 0,
			},
			true,
		},
		{
			"sample rate > 10000 is invalid",
			&types.ContinuousPoCParams{ValidationSampleRateBps: 10001},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.params.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ── ContinuousPoCCommit serialization round-trip ──────────────────────────────

func TestContinuousPoCCommit_MarshalRoundTrip(t *testing.T) {
	rootHash := make([]byte, 32)
	for i := range rootHash {
		rootHash[i] = byte(i)
	}
	original := &types.ContinuousPoCCommit{
		ParticipantAddress: "cosmos1qypqxpq9qcrsszgszyfpx9q4zct3sxfqelr5ey",
		EpochIndex:         42,
		NonceCount:         100,
		RootHash:           rootHash,
		InferenceCount:     5,
		CommitBlockHeight:  12345,
		GpuUtilizationBps:  3000,
	}

	bz, err := original.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, bz)

	decoded := &types.ContinuousPoCCommit{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.Equal(t, original.ParticipantAddress, decoded.ParticipantAddress)
	require.Equal(t, original.EpochIndex, decoded.EpochIndex)
	require.Equal(t, original.NonceCount, decoded.NonceCount)
	require.Equal(t, original.RootHash, decoded.RootHash)
	require.Equal(t, original.CommitBlockHeight, decoded.CommitBlockHeight)
	require.Equal(t, original.GpuUtilizationBps, decoded.GpuUtilizationBps)
}

// ── ContinuousPoCEpochSummary serialization round-trip ────────────────────────

func TestContinuousPoCEpochSummary_MarshalRoundTrip(t *testing.T) {
	original := &types.ContinuousPoCEpochSummary{
		ParticipantAddress: "cosmos1qypqxpq9qcrsszgszyfpx9q4zct3sxfqelr5ey",
		EpochIndex:         5,
		TotalNonces:        1000,
		TotalInferences:    50,
		CommitCount:        10,
		LastCommitHeight:   99,
		EffectivePocWeight: 100,
		PenaltyApplied:     false,
	}

	bz, err := original.Marshal()
	require.NoError(t, err)
	decoded := &types.ContinuousPoCEpochSummary{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.Equal(t, original.TotalNonces, decoded.TotalNonces)
	require.Equal(t, original.EffectivePocWeight, decoded.EffectivePocWeight)
	require.Equal(t, original.PenaltyApplied, decoded.PenaltyApplied)
}

func TestContinuousPoCEpochSummary_PenaltyRoundTrip(t *testing.T) {
	original := &types.ContinuousPoCEpochSummary{
		ParticipantAddress: "cosmos1abc",
		EpochIndex:         1,
		PenaltyApplied:     true,
	}
	bz, _ := original.Marshal()
	decoded := &types.ContinuousPoCEpochSummary{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.True(t, decoded.PenaltyApplied)
	require.Equal(t, int64(0), decoded.EffectivePocWeight)
}

// ── Merkle proof verification ─────────────────────────────────────────────────

func TestVerifyMerkleProof_SingleLeaf(t *testing.T) {
	// Single leaf: root = sha256(leaf_value), no siblings
	leafValue := []byte("nonce_preimage_0")
	expected := sha256.Sum256(leafValue)
	rootHash := expected[:]

	require.True(t, types.VerifyMerkleProof(leafValue, 0, nil, nil, rootHash))
}

func TestVerifyMerkleProof_TwoLeaves(t *testing.T) {
	// Tree:        root
	//             /    \
	//          H(L0)  H(L1)
	// root = sha256(H(L0) || H(L1))
	l0 := []byte("nonce_0")
	l1 := []byte("nonce_1")
	h0 := sha256.Sum256(l0)
	h1 := sha256.Sum256(l1)

	combined := append(h0[:], h1[:]...)
	root := sha256.Sum256(combined)
	rootHash := root[:]

	// Prove L0: sibling H(L1) on the right (dirs[0] = false)
	require.True(t, types.VerifyMerkleProof(l0, 0, [][]byte{h1[:]}, []bool{false}, rootHash))
	// Prove L1: sibling H(L0) on the left (dirs[0] = true)
	require.True(t, types.VerifyMerkleProof(l1, 1, [][]byte{h0[:]}, []bool{true}, rootHash))
	// Wrong leaf value fails
	require.False(t, types.VerifyMerkleProof([]byte("wrong"), 0, [][]byte{h1[:]}, []bool{false}, rootHash))
	// Wrong root fails
	require.False(t, types.VerifyMerkleProof(l0, 0, [][]byte{h1[:]}, []bool{false}, make([]byte, 32)))
}

func TestVerifyMerkleProof_MismatchedLengths(t *testing.T) {
	require.False(t, types.VerifyMerkleProof(
		[]byte("leaf"), 0,
		[][]byte{make([]byte, 32)}, // 1 sibling
		[]bool{},                   // 0 dirs
		make([]byte, 32),
	))
}

func TestVerifyMerkleProof_InvalidRootLength(t *testing.T) {
	require.False(t, types.VerifyMerkleProof([]byte("leaf"), 0, nil, nil, []byte("short")))
}

func TestVerifyMerkleProof_FourLeaves(t *testing.T) {
	// Full 4-leaf balanced tree:
	//         root
	//        /    \
	//      N01    N23
	//     /   \  /   \
	//   H0   H1 H2   H3
	leaves := [][]byte{
		[]byte("nonce_0"),
		[]byte("nonce_1"),
		[]byte("nonce_2"),
		[]byte("nonce_3"),
	}
	var hn [4][32]byte
	for i, l := range leaves {
		hn[i] = sha256.Sum256(l)
	}
	n01combined := append(hn[0][:], hn[1][:]...)
	n23combined := append(hn[2][:], hn[3][:]...)
	n01 := sha256.Sum256(n01combined)
	n23 := sha256.Sum256(n23combined)
	rootCombined := append(n01[:], n23[:]...)
	root := sha256.Sum256(rootCombined)
	rootHash := root[:]

	// Prove leaf 2 (index 2):
	// Path: H(L2) → n23 (sibling: H(L3), right=false) → root (sibling: n01, left=true)
	require.True(t, types.VerifyMerkleProof(
		leaves[2], 2,
		[][]byte{hn[3][:], n01[:]},
		[]bool{false, true},
		rootHash,
	))
}

// ── PruningState new fields round-trip ───────────────────────────────────────

func TestPruningState_NewFieldsRoundTrip(t *testing.T) {
	original := types.PruningState{
		PocBatchesPrunedEpoch:             1,
		PocValidationsPrunedEpoch:         2,
		InferencePrunedEpoch:              3,
		ContinuousPoCCommitsPrunedEpoch:   7,
		ContinuousPoCChallengePrunedEpoch: 8,
	}
	bz, err := original.Marshal()
	require.NoError(t, err)

	decoded := types.PruningState{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.Equal(t, original.ContinuousPoCCommitsPrunedEpoch, decoded.ContinuousPoCCommitsPrunedEpoch)
	require.Equal(t, original.ContinuousPoCChallengePrunedEpoch, decoded.ContinuousPoCChallengePrunedEpoch)
}

func TestPruningState_BackwardsCompatibility(t *testing.T) {
	// State written by old code (fields 1-3 only) must decode without error;
	// new fields default to 0.
	old := types.PruningState{
		PocBatchesPrunedEpoch:     10,
		PocValidationsPrunedEpoch: 20,
		InferencePrunedEpoch:      30,
	}
	bz, _ := old.Marshal()

	decoded := types.PruningState{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.Equal(t, int64(10), decoded.PocBatchesPrunedEpoch)
	require.Equal(t, int64(0), decoded.ContinuousPoCCommitsPrunedEpoch)
	require.Equal(t, int64(0), decoded.ContinuousPoCChallengePrunedEpoch)
}

// ── DefaultParams includes ContinuousPoCParams ────────────────────────────────

func TestDefaultParams_IncludesContinuousPoCParams(t *testing.T) {
	p := types.DefaultParams()
	require.NotNil(t, p.ContinuousPocParams)
	require.NoError(t, p.ContinuousPocParams.Validate())
	require.False(t, p.ContinuousPocParams.EnableContinuousPoC, "should be disabled by default")
}

// ── Params.Equal includes ContinuousPocParams ────────────────────────────────

func TestParams_EqualWithContinuousPoCParams(t *testing.T) {
	p1 := types.DefaultParams()
	p2 := types.DefaultParams()
	require.True(t, p1.Equal(p2))

	p2.ContinuousPocParams.EnableContinuousPoC = true
	require.False(t, p1.Equal(p2))
}

// ── Params Marshal/Unmarshal round-trip includes field 14 ────────────────────

func TestParams_MarshalIncludesContinuousPoCParams(t *testing.T) {
	p := types.DefaultParams()
	p.ContinuousPocParams.EnableContinuousPoC = true
	p.ContinuousPocParams.NonceWeight = 42

	bz, err := p.Marshal()
	require.NoError(t, err)
	require.NotEmpty(t, bz)

	// Field 14, wire type 2 tag = 0x72; must appear in serialized bytes.
	bzHex := hex.EncodeToString(bz)
	require.Contains(t, bzHex, "72", "serialized Params must contain field-14 tag (0x72)")

	decoded := types.Params{}
	require.NoError(t, decoded.Unmarshal(bz))
	require.NotNil(t, decoded.ContinuousPocParams)
	require.True(t, decoded.ContinuousPocParams.EnableContinuousPoC)
	require.Equal(t, uint32(42), decoded.ContinuousPocParams.NonceWeight)
}

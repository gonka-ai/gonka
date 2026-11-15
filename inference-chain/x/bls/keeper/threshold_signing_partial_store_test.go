package keeper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

type kvOp struct {
	Operation string `json:"operation"`
	Key       string `json:"key"`
}

// tracedOps runs f with the bls store traced and returns the operations.
func tracedOps(t *testing.T, ctx sdk.Context, f func(sdk.Context)) []kvOp {
	t.Helper()
	ms, ok := ctx.MultiStore().(interface {
		SetTracer(io.Writer) storetypes.MultiStore
	})
	require.True(t, ok)
	var buf bytes.Buffer
	ms.SetTracer(&buf)
	defer ms.SetTracer(nil)
	f(ctx)

	var ops []kvOp
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var op kvOp
		require.NoError(t, json.Unmarshal([]byte(line), &op))
		ops = append(ops, op)
	}
	return ops
}

// countOps counts matching keys; distinct ones for iterKey, which the
// prefix iterator logs on every Valid/Key call.
func countOps(t *testing.T, ops []kvOp, operation string, match func([]byte) bool) int {
	t.Helper()
	n := 0
	seen := map[string]bool{}
	for _, op := range ops {
		if op.Operation != operation {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(op.Key)
		require.NoError(t, err)
		if !match(key) {
			continue
		}
		if operation == "iterKey" {
			if seen[op.Key] {
				continue
			}
			seen[op.Key] = true
		}
		n++
	}
	return n
}

// A partial signature below the threshold writes only its own sub-key, and a
// submission after completion is rejected without reading the collected ones.
func TestAddPartialSignature_StoreAccessPerSigner(t *testing.T) {
	k, ctx := setupTimingKeeper(t)
	ctx = ctx.WithBlockHeight(10)
	_, _, _, g2Gen := bls12381.Generators()

	coeffs := make([]fr.Element, gkvDegree+1)
	for i := range coeffs {
		coeffs[i].SetUint64(uint64(11 + i))
	}
	totalSlots := uint32(gkvParticipants * gkvSlotsEach)
	scalars := computeSlotScalars(coeffs, totalSlots)
	epoch := types.EpochBLSData{
		EpochId:        1,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   gkvDegree,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: g2BytesFromScalar(g2Gen, coeffs[0]),
		Participants:   buildTimingParticipants(totalSlots, gkvParticipants),
	}
	for _, sk := range scalars {
		epoch.SlotPublicKeys = append(epoch.SlotPublicKeys, g2BytesFromScalar(g2Gen, sk))
	}
	require.NoError(t, k.SetEpochBLSData(ctx, epoch))

	// Bridge mint payload shape: five 32-byte fields.
	signingData := types.SigningData{
		CurrentEpochId: epoch.EpochId,
		ChainId:        bytes.Repeat([]byte{7}, 32),
		RequestId:      bytes.Repeat([]byte{8}, 32),
	}
	for i := byte(0); i < 5; i++ {
		signingData.Data = append(signingData.Data, bytes.Repeat([]byte{0x20 + i}, 32))
	}
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))
	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	msgG1, err := k.hashToG1(request.MessageHash)
	require.NoError(t, err)

	baseKey := types.ThresholdSigningRequestKey(signingData.RequestId)
	isBase := func(key []byte) bool { return bytes.Equal(key, baseKey) }
	partialPrefix := types.ThresholdPartialSigRequestPrefix(signingData.RequestId)
	isPartial := func(key []byte) bool { return bytes.HasPrefix(key, partialPrefix) }

	ms := msgServer{Keeper: k}
	threshold := int(gkvDegree/gkvSlotsEach) + 1 // 11th signer covers 44 >= 41 slots
	for i, p := range epoch.Participants {
		msg := &types.MsgSubmitPartialSignature{Creator: p.Address, RequestId: signingData.RequestId}
		for slot := p.SlotStartIndex; slot <= p.SlotEndIndex; slot++ {
			msg.SlotIndices = append(msg.SlotIndices, slot)
			msg.PartialSignature = append(msg.PartialSignature, g1SignatureFromScalar(msgG1, scalars[slot])...)
		}

		var gas storetypes.Gas
		var submitErr error
		ops := tracedOps(t, ctx, func(c sdk.Context) {
			sub := c.WithGasMeter(storetypes.NewInfiniteGasMeter())
			_, submitErr = ms.SubmitPartialSignature(sub, msg)
			gas = sub.GasMeter().GasConsumed()
		})
		baseWrites := countOps(t, ops, "write", isBase)
		partialReads := countOps(t, ops, "iterKey", isPartial)
		t.Logf("signer %2d: gas %d, base writes %d, partial sigs read %d, err %v", i+1, gas, baseWrites, partialReads, submitErr)

		switch {
		case i+1 < threshold:
			require.NoError(t, submitErr)
			require.Zero(t, baseWrites, "signer %d below the threshold must not rewrite the request", i+1)
			require.Equal(t, i, partialReads)
		case i+1 == threshold:
			require.NoError(t, submitErr)
			require.Equal(t, 1, baseWrites, "the completing signer stores the final signature")
		default:
			require.ErrorContains(t, submitErr, "not collecting signatures")
			require.Zero(t, partialReads, "a late signer must not read the collected partial signatures")
		}
	}

	final, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED, final.Status)
	require.Len(t, final.PartialSignatures, threshold)
	require.Equal(t, g1SignatureFromScalar(msgG1, coeffs[0]), final.FinalSignature)
}

// A request still carrying legacy inline partials keeps them on the
// rehydrated view and has them stripped from the base record, as before.
func TestAddPartialSignature_LegacyInlinePartialsStillRewriteBase(t *testing.T) {
	k, ctx := setupTimingKeeper(t)
	ctx = ctx.WithBlockHeight(10)
	_, _, _, g2Gen := bls12381.Generators()

	coeffs := make([]fr.Element, gkvDegree+1)
	for i := range coeffs {
		coeffs[i].SetUint64(uint64(5 + i))
	}
	totalSlots := uint32(gkvParticipants * gkvSlotsEach)
	scalars := computeSlotScalars(coeffs, totalSlots)
	epoch := types.EpochBLSData{
		EpochId:        1,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   gkvDegree,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: g2BytesFromScalar(g2Gen, coeffs[0]),
		Participants:   buildTimingParticipants(totalSlots, gkvParticipants),
	}
	for _, sk := range scalars {
		epoch.SlotPublicKeys = append(epoch.SlotPublicKeys, g2BytesFromScalar(g2Gen, sk))
	}
	require.NoError(t, k.SetEpochBLSData(ctx, epoch))

	signingData := types.SigningData{
		CurrentEpochId: epoch.EpochId,
		ChainId:        bytes.Repeat([]byte{7}, 32),
		RequestId:      bytes.Repeat([]byte{9}, 32),
		Data:           [][]byte{bytes.Repeat([]byte{1}, 32)},
	}
	require.NoError(t, k.RequestThresholdSignature(ctx, signingData))
	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	msgG1, err := k.hashToG1(request.MessageHash)
	require.NoError(t, err)

	sign := func(p types.BLSParticipantInfo) types.PartialSignature {
		ps := types.PartialSignature{ParticipantAddress: p.Address}
		for slot := p.SlotStartIndex; slot <= p.SlotEndIndex; slot++ {
			ps.SlotIndices = append(ps.SlotIndices, slot)
			ps.Signature = append(ps.Signature, g1SignatureFromScalar(msgG1, scalars[slot])...)
		}
		return ps
	}

	// Pre-split layout: the first signer's entry inline in the base record.
	legacy := *request
	legacy.PartialSignatures = []types.PartialSignature{sign(epoch.Participants[0])}
	raw, err := k.cdc.Marshal(&legacy)
	require.NoError(t, err)
	require.NoError(t, k.storeService.OpenKVStore(ctx).Set(types.ThresholdSigningRequestKey(signingData.RequestId), raw))

	second := sign(epoch.Participants[1])
	require.NoError(t, k.AddPartialSignature(ctx, signingData.RequestId, second.SlotIndices, second.Signature, second.ParticipantAddress))

	stored, err := k.getSigningRequestBase(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Empty(t, stored.PartialSignatures, "base record is rewritten without the inline entries")
	subKeys, err := k.ListThresholdPartialSignatures(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Len(t, subKeys, 1)
	require.Equal(t, second.ParticipantAddress, subKeys[0].ParticipantAddress)
}

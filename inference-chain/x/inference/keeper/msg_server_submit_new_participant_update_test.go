package keeper_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// New tests to ensure restricted updates when participant already exists
func TestMsgServer_SubmitNewParticipant_UpdateExistingRestrictedFields(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// initial keys
	oldValKey := base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())
	oldWorkKey := base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())

	// create participant first
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          "http://old.url",
		ValidatorKey: oldValKey,
		WorkerKey:    oldWorkKey,
	})
	require.NoError(t, err)

	p1, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)

	// new values for allowed fields
	newValKey := base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())
	newWorkKey := base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())
	newURL := "http://new.url"

	// resubmit with new allowed fields
	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          newURL,
		ValidatorKey: newValKey,
		WorkerKey:    newWorkKey,
	})
	require.NoError(t, err)

	p2, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)

	// Only allowed fields should change
	require.Equal(t, newURL, p2.InferenceUrl)
	require.Equal(t, newValKey, p2.ValidatorKey)
	require.Equal(t, newWorkKey, p2.WorkerPublicKey)

	// All other fields should remain the same
	require.Equal(t, p1.Index, p2.Index)
	require.Equal(t, p1.Address, p2.Address)
	require.Equal(t, p1.Weight, p2.Weight)
	require.Equal(t, p1.JoinTime, p2.JoinTime)
	require.Equal(t, p1.JoinHeight, p2.JoinHeight)
	require.Equal(t, p1.LastInferenceTime, p2.LastInferenceTime)
	require.Equal(t, p1.Status, p2.Status)
	require.Equal(t, p1.CoinBalance, p2.CoinBalance)
	require.Equal(t, p1.ConsecutiveInvalidInferences, p2.ConsecutiveInvalidInferences)
	require.Equal(t, p1.EpochsCompleted, p2.EpochsCompleted)
	require.Equal(t, p1.CurrentEpochStats, p2.CurrentEpochStats)
}

func TestMsgServer_SubmitNewParticipant_EmptyValuesNoEffect(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	valKey := base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())
	workKey := base64.StdEncoding.EncodeToString(secp256k1.GenPrivKey().PubKey().Bytes())

	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          "http://url",
		ValidatorKey: valKey,
		WorkerKey:    workKey,
	})
	require.NoError(t, err)

	p1, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)

	// update to empty values
	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator:      testutil.Executor,
		Url:          "",
		ValidatorKey: "",
		WorkerKey:    "",
	})
	require.NoError(t, err)

	p2, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)

	require.Equal(t, "http://url", p2.InferenceUrl)
	require.Equal(t, valKey, p2.ValidatorKey)
	require.Equal(t, workKey, p2.WorkerPublicKey)

	// Other fields unchanged
	require.Equal(t, p1.Index, p2.Index)
	require.Equal(t, p1.Address, p2.Address)
	require.Equal(t, p1.Weight, p2.Weight)
	require.Equal(t, p1.JoinTime, p2.JoinTime)
	require.Equal(t, p1.JoinHeight, p2.JoinHeight)
	require.Equal(t, p1.LastInferenceTime, p2.LastInferenceTime)
	require.Equal(t, p1.Status, p2.Status)
	require.Equal(t, p1.CoinBalance, p2.CoinBalance)
	require.Equal(t, p1.ConsecutiveInvalidInferences, p2.ConsecutiveInvalidInferences)
	require.Equal(t, p1.EpochsCompleted, p2.EpochsCompleted)
	require.Equal(t, p1.CurrentEpochStats, p2.CurrentEpochStats)
}

// Registration and re-registration read the participant once: the status check reuses that read.
func TestMsgServer_SubmitNewParticipant_ReadsParticipantOnce(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	m := k.Participants
	key, err := collections.EncodeKeyWithPrefix(m.GetPrefix(), m.KeyCodec(), sdk.MustAccAddressFromBech32(testutil.Executor))
	require.NoError(t, err)
	readOp := `"operation":"read","key":"` + base64.StdEncoding.EncodeToString(key) + `"`

	for _, url := range []string{"http://old.url", "http://new.url"} {
		var trace bytes.Buffer
		sdkCtx.MultiStore().SetTracer(&trace)
		gasBefore := sdkCtx.GasMeter().GasConsumed()
		_, err := ms.SubmitNewParticipant(sdkCtx, &types.MsgSubmitNewParticipant{Creator: testutil.Executor, Url: url})
		t.Logf("%s: gas %d", url, sdkCtx.GasMeter().GasConsumed()-gasBefore)
		sdkCtx.MultiStore().SetTracer(nil)
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(trace.String(), readOp), url)
		p, found := k.GetParticipant(sdkCtx, testutil.Executor)
		require.True(t, found)
		require.Equal(t, url, p.InferenceUrl)
	}
}

package simulation_test

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/types/kv"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/simulation"
	"github.com/productscience/inference/x/inference/types"
)

// keyWithPrefix builds a sample KV key by appending an arbitrary suffix
// after the collection prefix. The decoder switch only inspects the
// prefix, so the suffix bytes are not interpreted.
func keyWithPrefix(prefix []byte, suffix string) []byte {
	out := make([]byte, 0, len(prefix)+len(suffix))
	out = append(out, prefix...)
	out = append(out, suffix...)
	return out
}

func newTestCodec() codec.BinaryCodec {
	return codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
}

func TestNewDecodeStore_Participant_DecodesProto(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	a := &types.Participant{Address: "gonka1aaaa", CoinBalance: 100}
	b := &types.Participant{Address: "gonka1aaaa", CoinBalance: 200}

	out := dec(
		kv.Pair{Key: keyWithPrefix(types.ParticipantsPrefix, "k"), Value: cdc.MustMarshal(a)},
		kv.Pair{Key: keyWithPrefix(types.ParticipantsPrefix, "k"), Value: cdc.MustMarshal(b)},
	)
	require.True(t, strings.HasPrefix(out, "Participant\n"), "label missing: %q", out)
	require.Contains(t, out, "100")
	require.Contains(t, out, "200")
}

func TestNewDecodeStore_Inference_DecodesProto(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	a := &types.Inference{InferenceId: "id-A", Status: types.InferenceStatus_STARTED}
	b := &types.Inference{InferenceId: "id-B", Status: types.InferenceStatus_FINISHED}

	out := dec(
		kv.Pair{Key: keyWithPrefix(types.InferencesPrefix, "id-A"), Value: cdc.MustMarshal(a)},
		kv.Pair{Key: keyWithPrefix(types.InferencesPrefix, "id-A"), Value: cdc.MustMarshal(b)},
	)
	require.True(t, strings.HasPrefix(out, "Inference\n"))
	require.Contains(t, out, "id-A")
	require.Contains(t, out, "id-B")
}

func TestNewDecodeStore_KeySet_PrintsKeyHex(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	keyA := keyWithPrefix(types.ActiveInvalidationsPrefix, "a-entry")
	keyB := keyWithPrefix(types.ActiveInvalidationsPrefix, "b-entry")

	out := dec(
		kv.Pair{Key: keyA, Value: nil},
		kv.Pair{Key: keyB, Value: nil},
	)
	require.True(t, strings.HasPrefix(out, "KeySet entry\n"))
	require.Contains(t, out, fmt.Sprintf("%X", keyA))
	require.Contains(t, out, fmt.Sprintf("%X", keyB))
}

func TestNewDecodeStore_Uint64_EffectiveEpochIndex(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	bufA := make([]byte, 8)
	bufB := make([]byte, 8)
	binary.BigEndian.PutUint64(bufA, 42)
	binary.BigEndian.PutUint64(bufB, 43)

	out := dec(
		kv.Pair{Key: types.EffectiveEpochIndexPrefix, Value: bufA},
		kv.Pair{Key: types.EffectiveEpochIndexPrefix, Value: bufB},
	)
	require.True(t, strings.HasPrefix(out, "uint64\n"))
	require.Contains(t, out, "42")
	require.Contains(t, out, "43")
}

func TestNewDecodeStore_Int64_LastUpgradeHeight(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	bufA := make([]byte, 8)
	bufB := make([]byte, 8)
	binary.BigEndian.PutUint64(bufA, uint64(int64(100)))
	binary.BigEndian.PutUint64(bufB, uint64(int64(200)))

	out := dec(
		kv.Pair{Key: types.LastUpgradeHeightPrefix, Value: bufA},
		kv.Pair{Key: types.LastUpgradeHeightPrefix, Value: bufB},
	)
	require.True(t, strings.HasPrefix(out, "int64\n"))
	require.Contains(t, out, "100")
	require.Contains(t, out, "200")
}

func TestNewDecodeStore_LegacyParamsKey(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	defaultParams := types.DefaultParams()
	out := dec(
		kv.Pair{Key: types.ParamsKey, Value: cdc.MustMarshal(&defaultParams)},
		kv.Pair{Key: types.ParamsKey, Value: cdc.MustMarshal(&defaultParams)},
	)
	require.True(t, strings.HasPrefix(out, "Params\n"), "label missing: %q", out)
}

func TestNewDecodeStore_UnknownPrefix_FallsBackToHex(t *testing.T) {
	cdc := newTestCodec()
	dec := simulation.NewDecodeStore(cdc)

	// 0xFE is intentionally outside any used prefix range — would conflict
	// with no current collection.
	keyA := []byte{0xFE, 'a'}
	keyB := []byte{0xFE, 'b'}
	out := dec(
		kv.Pair{Key: keyA, Value: []byte{1, 2}},
		kv.Pair{Key: keyB, Value: []byte{3, 4}},
	)
	require.True(t, strings.HasPrefix(out, "unhandled inference prefix\n"))
	require.Contains(t, out, fmt.Sprintf("%X", keyA))
	require.Contains(t, out, fmt.Sprintf("%X", keyB))
}

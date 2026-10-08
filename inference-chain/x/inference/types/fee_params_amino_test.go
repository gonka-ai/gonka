package types_test

import (
	"encoding/json"
	"testing"

	aminojson "cosmossdk.io/x/tx/signing/aminojson"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	inferenceapi "github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

func TestFeeParamsAminoJSON(t *testing.T) {
	params := types.DefaultParams()
	require.NoError(t, params.Validate())

	bz := marshalQueryParamsAminoJSON(t, params)
	require.Contains(t, string(bz), `"type":"inference/MsgGasRule/StoredDelta"`)
	require.Contains(t, string(bz), `"type":"inference/MsgGasRule/StoredBytes"`)
	require.Contains(t, string(bz), `"stored_delta"`)
	require.Contains(t, string(bz), `"stored_bytes"`)
	// SDK camel-cases the oneof name, so Amino JSON uses "Func" not "func".
	require.Contains(t, string(bz), `"Func"`)

	params.FeeParams.Groups[0].Msgs = append(params.FeeParams.Groups[0].Msgs, &types.MsgGasRule{
		TypeUrl: sdk.MsgTypeURL(&types.MsgSubmitPocValidationsV2{}),
		Func: &types.MsgGasRule_RepeatedLen{
			RepeatedLen: &types.RepeatedLenParams{GasPerUnit: 10, Field: "validations"},
		},
	})
	require.NoError(t, params.Validate())
	bz = marshalQueryParamsAminoJSON(t, params)
	require.Contains(t, string(bz), `"type":"inference/MsgGasRule/RepeatedLen"`)
	require.Contains(t, string(bz), `"repeated_len"`)
}

func marshalQueryParamsAminoJSON(t *testing.T, params types.Params) []byte {
	t.Helper()
	bin, err := params.Marshal()
	require.NoError(t, err)

	apiParams := &inferenceapi.Params{}
	require.NoError(t, proto.Unmarshal(bin, apiParams))

	out, err := aminojson.NewEncoder(aminojson.EncoderOptions{}).Marshal(&inferenceapi.QueryParamsResponse{
		Params: apiParams,
	})
	require.NoError(t, err)
	require.True(t, json.Valid(out), "AutoCLI --output json must emit valid JSON")
	return out
}

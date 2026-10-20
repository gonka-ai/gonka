package types

import (
	"strings"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestValidateTrainingEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		valid    bool
	}{
		{name: "https host", endpoint: "https://a.example.com", valid: true},
		{name: "http host with port", endpoint: "http://10.0.0.5:9700", valid: true},
		{name: "path prefix for a proxy route", endpoint: "https://a.example.com/trainshard-node2", valid: true},
		{name: "trailing slash", endpoint: "https://a.example.com/", valid: false},
		{name: "no scheme", endpoint: "a.example.com:9700", valid: false},
		{name: "other scheme", endpoint: "ftp://a.example.com", valid: false},
		{name: "query", endpoint: "https://a.example.com?x=1", valid: false},
		{name: "fragment", endpoint: "https://a.example.com#x", valid: false},
		{name: "bare query mark", endpoint: "https://a.example.com?", valid: false},
		{name: "bare fragment mark", endpoint: "https://a.example.com#", valid: false},
		{name: "credentials", endpoint: "https://user:pw@a.example.com", valid: false},
		{name: "empty host", endpoint: "https://", valid: false},
		{name: "port zero", endpoint: "http://10.0.0.5:0", valid: false},
		{name: "colon without a port", endpoint: "http://10.0.0.5:", valid: false},
		{name: "colon without a port before a path", endpoint: "http://10.0.0.5:/t", valid: false},
		{name: "port out of range", endpoint: "http://10.0.0.5:70000", valid: false},
		{name: "too long", endpoint: "https://" + strings.Repeat("a", 250) + ".com", valid: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTrainingEndpoint(tc.endpoint)
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrTrainshardOptInRequest)
		})
	}
}

func TestValidateTrainingNodeId(t *testing.T) {
	for _, nodeId := range []string{"node-1.a_b", "A", "...", strings.Repeat("n", 64)} {
		require.NoError(t, ValidateTrainingNodeId(nodeId), nodeId)
	}
	for _, nodeId := range []string{"", ".", "..", "node/1", `node\1`, "node 1", " node-1", "nöde", strings.Repeat("n", 65)} {
		require.ErrorIs(t, ValidateTrainingNodeId(nodeId), ErrTrainshardOptInRequest, nodeId)
	}
}

func TestMsgAutokickTrainshardNode_ValidateBasicBoundsTheRequestId(t *testing.T) {
	msg := MsgAutokickTrainshardNode{
		Creator:     sample.AccAddress(),
		Participant: sample.AccAddress(),
		NodeId:      "node-a",
		RequestId:   strings.Repeat("r", MaxAutokickRequestIdLen),
	}
	require.NoError(t, msg.ValidateBasic())

	msg.RequestId += "r"
	require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidRequest)
	msg.RequestId = ""
	require.ErrorIs(t, msg.ValidateBasic(), sdkerrors.ErrInvalidRequest)
}

func TestMsgRefreshTrainingNodeOptIn_ValidateBasicChecksTheNodeIds(t *testing.T) {
	msg := MsgRefreshTrainingNodeOptIn{Creator: sample.AccAddress(), NodeIds: []string{"node-1.a_b"}}
	require.NoError(t, msg.ValidateBasic())

	for _, nodeId := range []string{"node/1", "..", "nöde", strings.Repeat("n", 65)} {
		msg.NodeIds = []string{"node-a", nodeId}
		require.ErrorIs(t, msg.ValidateBasic(), ErrTrainshardOptInRequest, nodeId)
	}
}

func TestMsgRefreshTrainingNodeOptIn_ValidateBasicChecksTheEndpoint(t *testing.T) {
	valid := MsgRefreshTrainingNodeOptIn{Creator: sample.AccAddress(), NodeIds: []string{"node-a"}}
	require.NoError(t, valid.ValidateBasic(), "an empty endpoint is allowed")

	valid.Endpoint = "https://a.example.com"
	require.NoError(t, valid.ValidateBasic())

	valid.Endpoint = "a.example.com"
	require.ErrorIs(t, valid.ValidateBasic(), ErrTrainshardOptInRequest)
}

package apiconfig_test

import (
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func validOverrideNode() apiconfig.InferenceNodeConfig {
	return apiconfig.InferenceNodeConfig{
		Id:            "node-1",
		Host:          "mlnode",
		InferencePort: 5000,
		PoCPort:       8080,
		MaxConcurrent: 1,
		Models: map[string]apiconfig.ModelConfig{
			"MiniMaxAI/MiniMax-M2.7": {
				ModelOverride: &apiconfig.ModelOverride{
					HfRepo:   "host/custom-minimax",
					HfCommit: "0123456789abcdef0123456789abcdef01234567",
				},
			},
		},
	}
}

func TestValidateInferenceNodeBasic_ModelOverride(t *testing.T) {
	require.Empty(t, apiconfig.ValidateInferenceNodeBasic(validOverrideNode()))

	node := validOverrideNode()
	config := node.Models["MiniMaxAI/MiniMax-M2.7"]
	config.ModelOverride.HfCommit = ""
	node.Models["MiniMaxAI/MiniMax-M2.7"] = config
	require.Contains(t, apiconfig.ValidateInferenceNodeBasic(node),
		"model MiniMaxAI/MiniMax-M2.7 override hf_commit is required")

	node = validOverrideNode()
	config = node.Models["MiniMaxAI/MiniMax-M2.7"]
	config.ModelOverride.HfCommit = "   "
	node.Models["MiniMaxAI/MiniMax-M2.7"] = config
	require.Contains(t, apiconfig.ValidateInferenceNodeBasic(node),
		"model MiniMaxAI/MiniMax-M2.7 override hf_commit is required")

	node = validOverrideNode()
	config = node.Models["MiniMaxAI/MiniMax-M2.7"]
	config.ModelOverride.HfCommit = "main"
	node.Models["MiniMaxAI/MiniMax-M2.7"] = config
	require.Contains(t, apiconfig.ValidateInferenceNodeBasic(node),
		"model MiniMaxAI/MiniMax-M2.7 override hf_commit must be a 40-character lowercase hexadecimal commit hash")

	node = validOverrideNode()
	config = node.Models["MiniMaxAI/MiniMax-M2.7"]
	config.ModelOverride.HfCommit = " 0123456789abcdef0123456789abcdef01234567 "
	node.Models["MiniMaxAI/MiniMax-M2.7"] = config
	require.Contains(t, apiconfig.ValidateInferenceNodeBasic(node),
		"model MiniMaxAI/MiniMax-M2.7 override hf_commit must be a 40-character lowercase hexadecimal commit hash")

	node = validOverrideNode()
	config = node.Models["MiniMaxAI/MiniMax-M2.7"]
	config.Args = []string{"--revision=main"}
	node.Models["MiniMaxAI/MiniMax-M2.7"] = config
	require.Contains(t, apiconfig.ValidateInferenceNodeBasic(node),
		"model MiniMaxAI/MiniMax-M2.7 override cannot use reserved argument --revision")
}

func TestInferenceNodeConfigDeepCopy_CopiesModelOverride(t *testing.T) {
	node := validOverrideNode()
	copy := node.DeepCopy()

	config := copy.Models["MiniMaxAI/MiniMax-M2.7"]
	config.ModelOverride.HfRepo = "changed/repo"
	copy.Models["MiniMaxAI/MiniMax-M2.7"] = config

	require.Equal(t, "host/custom-minimax",
		node.Models["MiniMaxAI/MiniMax-M2.7"].ModelOverride.HfRepo)
}

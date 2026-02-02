package broker

import (
	"os"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/assert"
)

// restoreEnforcedModelEnv restores the env var to disabled state after test
func restoreEnforcedModelEnv() {
	os.Setenv("ENFORCED_MODEL_ID", "disabled")
	os.Unsetenv("ENFORCED_MODEL_ARGS")
}

func TestGetEnforcedModel_Defaults(t *testing.T) {
	os.Unsetenv("ENFORCED_MODEL_ID")
	os.Unsetenv("ENFORCED_MODEL_ARGS")
	defer restoreEnforcedModelEnv()

	modelId, args := getEnforcedModel()

	assert.Equal(t, defaultEnforcedModelId, modelId)
	assert.Equal(t, defaultEnforcedModelArgs, args)
}

func TestGetEnforcedModel_CustomValues(t *testing.T) {
	os.Setenv("ENFORCED_MODEL_ID", "custom/model")
	os.Setenv("ENFORCED_MODEL_ARGS", "--arg1 --arg2 value")
	defer restoreEnforcedModelEnv()

	modelId, args := getEnforcedModel()

	assert.Equal(t, "custom/model", modelId)
	assert.Equal(t, []string{"--arg1", "--arg2", "value"}, args)
}

func TestGetEnforcedModel_Disabled(t *testing.T) {
	testCases := []string{"disabled", "none"}

	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			os.Setenv("ENFORCED_MODEL_ID", tc)
			defer restoreEnforcedModelEnv()

			modelId, args := getEnforcedModel()

			assert.Empty(t, modelId)
			assert.Nil(t, args)
		})
	}
}

func TestEnforceModel_Disabled(t *testing.T) {
	os.Setenv("ENFORCED_MODEL_ID", "disabled")

	node := &apiconfig.InferenceNodeConfig{
		Models: map[string]apiconfig.ModelConfig{
			"other-model": {Args: []string{"--original"}},
		},
	}

	EnforceModel(node)

	assert.Contains(t, node.Models, "other-model")
	assert.NotContains(t, node.Models, defaultEnforcedModelId)
}

func TestEnforceModel_SkipsIfModelExists(t *testing.T) {
	os.Setenv("ENFORCED_MODEL_ID", "test-model")
	defer restoreEnforcedModelEnv()

	node := &apiconfig.InferenceNodeConfig{
		Models: map[string]apiconfig.ModelConfig{
			"test-model": {Args: []string{"--custom-arg"}},
		},
	}

	EnforceModel(node)

	// Should preserve existing args
	assert.Equal(t, []string{"--custom-arg"}, node.Models["test-model"].Args)
}

func TestEnforceModel_ReplacesIfModelMissing(t *testing.T) {
	os.Setenv("ENFORCED_MODEL_ID", "enforced-model")
	os.Setenv("ENFORCED_MODEL_ARGS", "--enforced-arg")
	defer restoreEnforcedModelEnv()

	node := &apiconfig.InferenceNodeConfig{
		Models: map[string]apiconfig.ModelConfig{
			"other-model": {Args: []string{"--other"}},
		},
	}

	EnforceModel(node)

	assert.Contains(t, node.Models, "enforced-model")
	assert.NotContains(t, node.Models, "other-model")
	assert.Equal(t, []string{"--enforced-arg"}, node.Models["enforced-model"].Args)
}

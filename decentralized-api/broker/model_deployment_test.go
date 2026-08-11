package broker

import (
	"testing"

	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestResolveModelDeployment_DefaultPreservesModelID(t *testing.T) {
	b := &Broker{}
	deployment := b.ResolveModelDeployment(types.Model{
		Id:        "governance/model",
		ModelArgs: []string{"--max-model-len", "4096"},
	}, ModelArgs{Args: []string{"--tensor-parallel-size", "2"}})

	require.Equal(t, "governance/model", deployment.GovernanceID)
	require.Equal(t, "governance/model", deployment.LoadModel)
	require.Empty(t, deployment.LoadCommit)
	require.Equal(t, []string{
		"--max-model-len", "4096",
		"--tensor-parallel-size", "2",
	}, deployment.Args)
}

func TestResolveModelDeployment_OverrideOwnsDeploymentFlags(t *testing.T) {
	b := &Broker{}
	deployment := b.ResolveModelDeployment(types.Model{
		Id: "MiniMaxAI/MiniMax-M2.7",
		ModelArgs: []string{
			"--revision=old",
			"--served-model-name", "old-alias", "second-alias",
			"--max-model-len", "4096",
		},
	}, ModelArgs{
		Args: []string{"--model=old/repo", "--tensor-parallel-size", "2"},
		ModelOverride: &apiconfig.ModelOverride{
			HfRepo:   "host/custom-minimax",
			HfCommit: "0123456789abcdef0123456789abcdef01234567",
		},
	})

	require.Equal(t, "host/custom-minimax", deployment.LoadModel)
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", deployment.LoadCommit)
	require.Equal(t, []string{
		"--max-model-len", "4096",
		"--tensor-parallel-size", "2",
		"--revision", "0123456789abcdef0123456789abcdef01234567",
		"--served-model-name", "MiniMaxAI/MiniMax-M2.7",
	}, deployment.Args)
	require.Len(t, deployment.Fingerprint(), 64)
}

func TestResolveModelDeployment_UnpinnedOverrideOmitsRevision(t *testing.T) {
	b := &Broker{}
	deployment := b.ResolveModelDeployment(types.Model{Id: "model-a"}, ModelArgs{
		ModelOverride: &apiconfig.ModelOverride{HfRepo: "host/model-a"},
	})

	require.Equal(t, []string{"--served-model-name", "model-a"}, deployment.Args)
}

func TestLoadedModelsContain(t *testing.T) {
	require.True(t, loadedModelsContain([]string{"alias-a", "alias-b"}, "alias-b"))
	require.False(t, loadedModelsContain([]string{"alias-a"}, "missing"))
}

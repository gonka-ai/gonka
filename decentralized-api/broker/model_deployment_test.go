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

func TestLoadedModelsContain(t *testing.T) {
	require.True(t, loadedModelsContain([]string{"alias-a", "alias-b"}, "alias-b"))
	require.False(t, loadedModelsContain([]string{"alias-a"}, "missing"))
}

func TestActiveDeploymentChanged(t *testing.T) {
	epoch := map[string]types.MLNodeInfo{"model-a": {NodeId: "node-1"}}
	old := map[string]ModelArgs{
		"model-a": {Args: []string{"--a"}},
		"model-b": {Args: []string{"--old"}},
	}

	changed, removed := activeDeploymentChanged(epoch, old, map[string]ModelArgs{
		"model-a": {Args: []string{"--a"}},
		"model-b": {Args: []string{"--new"}},
	})
	require.False(t, changed)
	require.False(t, removed)

	changed, removed = activeDeploymentChanged(epoch, old, map[string]ModelArgs{
		"model-a": {Args: []string{"--changed"}},
		"model-b": {Args: []string{"--old"}},
	})
	require.True(t, changed)
	require.False(t, removed)

	changed, removed = activeDeploymentChanged(epoch, old, map[string]ModelArgs{
		"model-b": {Args: []string{"--old"}},
	})
	require.False(t, changed)
	require.True(t, removed)

	changed, removed = activeDeploymentChanged(nil, map[string]ModelArgs{"model-a": {}}, map[string]ModelArgs{"model-b": {}})
	require.True(t, changed)
	require.False(t, removed)
}

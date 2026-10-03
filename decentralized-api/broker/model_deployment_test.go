package broker

import (
	"encoding/json"
	"os"
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

func TestResolveModelDeployment_MaxModelLenOverride(t *testing.T) {
	tests := []struct {
		name       string
		epoch      []string
		local      []string
		want       []string
		suppressed []string
	}{
		{
			name:       "local equals value",
			epoch:      []string{"--max-model-len", "240000"},
			local:      []string{"--max-model-len=8192"},
			want:       []string{"--max-model-len", "240000"},
			suppressed: []string{"--max-model-len"},
		},
		{
			name:       "epoch equals value",
			epoch:      []string{"--max-model-len=240000"},
			local:      []string{"--max-model-len", "8192"},
			want:       []string{"--max-model-len=240000"},
			suppressed: []string{"--max-model-len"},
		},
		{
			name:       "underscore alias",
			epoch:      []string{"--max-model-len", "240000"},
			local:      []string{"--max_model_len=8192", "--max_model_len", "4096"},
			want:       []string{"--max-model-len", "240000"},
			suppressed: []string{"--max_model_len"},
		},
		{
			name:  "local abbreviation preserves other local options",
			epoch: []string{"--max-model-len", "240000"},
			local: []string{"--gpu-memory-utilization", "0.9", "--max-model", "8192"},
			want: []string{
				"--max-model-len", "240000", "--gpu-memory-utilization", "0.9",
			},
			suppressed: []string{"--max-model"},
		},
		{
			name:       "epoch abbreviation comes last",
			epoch:      []string{"--max-model", "240000"},
			local:      []string{"--max-model-len=8192"},
			want:       []string{"--max-model", "240000"},
			suppressed: []string{"--max-model-len"},
		},
		{
			name:       "prefix overlap does not discard local option",
			epoch:      []string{"--foo-bar", "epoch"},
			local:      []string{"--foo", "local"},
			want:       []string{"--foo", "local", "--foo-bar", "epoch"},
			suppressed: []string{"--foo"},
		},
		{
			name:       "ambiguous prefix stays for parser validation",
			epoch:      []string{"--foo-bar", "epoch", "--foo-baz", "other-epoch"},
			local:      []string{"--foo", "local"},
			want:       []string{"--foo", "local", "--foo-bar", "epoch", "--foo-baz", "other-epoch"},
			suppressed: []string{"--foo"},
		},
		{
			name:       "dotted options share their root",
			epoch:      []string{"--foo.bar_baz", "epoch"},
			local:      []string{"--foo.bar-baz", "local"},
			want:       []string{"--foo.bar_baz", "epoch"},
			suppressed: []string{"--foo.bar-baz"},
		},
		{
			name:       "other parameter matches across forms",
			epoch:      []string{"--tensor-parallel-size=4"},
			local:      []string{"--tensor_parallel_size", "2", "--gpu-memory-utilization", "0.9"},
			want:       []string{"--tensor-parallel-size=4", "--gpu-memory-utilization", "0.9"},
			suppressed: []string{"--tensor_parallel_size"},
		},
		{
			name:       "inline value does not consume a short option",
			epoch:      []string{"--max-model-len", "240000"},
			local:      []string{"--max-model-len=8192", "-tp", "2"},
			want:       []string{"--max-model-len", "240000", "-tp", "2"},
			suppressed: []string{"--max-model-len"},
		},
		{
			name:  "end of options stays at the end",
			epoch: []string{"--max-model-len", "240000"},
			local: []string{"--", "--max-model-len", "literal"},
			want:  []string{"--max-model-len", "240000", "--", "--max-model-len", "literal"},
		},
		{
			name:  "known full options do not trigger prefix reordering",
			epoch: []string{"--reasoning-parser-plugin", "plugin"},
			local: []string{"--reasoning-parser", "reasoner"},
			want:  []string{"--reasoning-parser-plugin", "plugin", "--reasoning-parser", "reasoner"},
		},
		{
			name:  "kv cache options remain independent",
			epoch: []string{"--kv-cache-dtype-skip-layers", "2"},
			local: []string{"--kv-cache-dtype", "fp8"},
			want:  []string{"--kv-cache-dtype-skip-layers", "2", "--kv-cache-dtype", "fp8"},
		},
		{
			name:  "tokenizer options remain independent",
			epoch: []string{"--tokenizer-mode", "auto"},
			local: []string{"--tokenizer", "repo"},
			want:  []string{"--tokenizer-mode", "auto", "--tokenizer", "repo"},
		},
		{
			name:  "quantization options remain independent",
			epoch: []string{"--quantization-config", "config"},
			local: []string{"--quantization", "fp8"},
			want:  []string{"--quantization-config", "config", "--quantization", "fp8"},
		},
		{
			name:       "negative boolean form is suppressed",
			epoch:      []string{"--enable-auto-tool-choice"},
			local:      []string{"--no-enable-auto-tool-choice"},
			want:       []string{"--enable-auto-tool-choice"},
			suppressed: []string{"--no-enable-auto-tool-choice"},
		},
		{
			name:       "short tensor alias is suppressed",
			epoch:      []string{"--tensor-parallel-size=4"},
			local:      []string{"-tp", "2"},
			want:       []string{"--tensor-parallel-size=4"},
			suppressed: []string{"-tp"},
		},
		{
			name:       "short optimization level is suppressed",
			epoch:      []string{"--optimization-level", "2"},
			local:      []string{"-O3"},
			want:       []string{"--optimization-level", "2"},
			suppressed: []string{"-O"},
		},
		{
			name:  "identical local value needs no warning",
			epoch: []string{"--max-model-len", "240000"},
			local: []string{"--max-model-len=240000"},
			want:  []string{"--max-model-len", "240000"},
		},
		{
			name:       "all values of a replaced option are removed",
			epoch:      []string{"--kv-cache-dtype-skip-layers", "0", "1"},
			local:      []string{"--kv-cache-dtype-skip-layers", "2", "3", "--max-num-seqs", "128"},
			want:       []string{"--kv-cache-dtype-skip-layers", "0", "1", "--max-num-seqs", "128"},
			suppressed: []string{"--kv-cache-dtype-skip-layers"},
		},
		{
			name:  "negative fractional value stays with its option",
			epoch: []string{"--max-model-len", "240000"},
			local: []string{"--rope-scaling.factor", "-.5"},
			want:  []string{"--max-model-len", "240000", "--rope-scaling.factor", "-.5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Broker{}
			deployment := b.ResolveModelDeployment(types.Model{
				Id: "governance/model", ModelArgs: tt.epoch,
			}, ModelArgs{Args: tt.local})
			require.Equal(t, tt.want, deployment.Args)
			_, suppressed := mergeModelArgs(tt.epoch, tt.local)
			if tt.suppressed == nil {
				require.Empty(t, suppressed)
			} else {
				require.Equal(t, tt.suppressed, suppressed)
			}
		})
	}
}

func TestMergeModelArgs_FullNamesThatSharePrefixes(t *testing.T) {
	pairs := [][2]string{
		{"--chat-template", "--chat-template-content-format"},
		{"--config", "--config-format"},
		{"--data-parallel-size", "--data-parallel-size-local"},
		{"--kv-cache-metrics", "--kv-cache-metrics-sample"},
		{"--model", "--model-impl"},
		{"--model", "--model-class-overrides"},
		{"--model", "--model-loader-extra-config"},
		{"--numa-bind", "--numa-bind-cpus"},
		{"--numa-bind", "--numa-bind-nodes"},
		{"--tokenizer", "--tokenizer-revision"},
	}
	for _, pair := range pairs {
		epoch := []string{pair[1], "epoch"}
		local := []string{pair[0], "local"}
		merged, suppressed := mergeModelArgs(epoch, local)
		require.Equal(t, append(epoch, local...), merged, pair)
		require.Empty(t, suppressed, pair)
	}
}

func TestModelArgKey_ShortAliases(t *testing.T) {
	aliases := map[string]string{
		"-asc": "--api-server-count", "-q": "--quantization",
		"-pp": "--pipeline-parallel-size", "-n": "--nnodes",
		"-r": "--node-rank", "-tp": "--tensor-parallel-size",
		"-dcp": "--decode-context-parallel-size", "-pcp": "--prefill-context-parallel-size",
		"-dp": "--data-parallel-size", "-dpn": "--data-parallel-rank",
		"-dpr": "--data-parallel-start-rank", "-dpl": "--data-parallel-size-local",
		"-dpa": "--data-parallel-address", "-dpp": "--data-parallel-rpc-port",
		"-dpb": "--data-parallel-backend", "-dph": "--data-parallel-hybrid-lb",
		"-dpe": "--data-parallel-external-lb", "-dpm": "--data-parallel-multi-port-external-lb",
		"-ep": "--enable-expert-parallel", "-sc": "--speculative-config",
		"-dc": "--diffusion-config", "-cc": "--compilation-config",
		"-ac": "--attention-config", "-O3": "--optimization-level",
	}
	for alias, key := range aliases {
		require.Equal(t, key, modelArgKey(alias), alias)
	}
}

func TestResolveModelDeployment_ExistingNodeConfigsKeepFingerprint(t *testing.T) {
	tests := []struct {
		file        string
		model       string
		tp          string
		maxModelLen string
		fingerprint string
	}{
		{"node-config-minimaxm27-H100.json", "MiniMaxAI/MiniMax-M2.7", "4", "180000", "d2c95c97aab8bfdcdb01dec24264659f62133fbc983984e6f2f64645d5ecebbc"},
		{"node-config-kimik26-H200.json", "moonshotai/Kimi-K2.6", "8", "240000", "c66432f6beede518791b091b08e67bc629f252d05e8dee294b3e9d8167443681"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile("../../deploy/join/" + tt.file)
			require.NoError(t, err)
			var nodes []struct {
				Models map[string]struct {
					Args []string `json:"args"`
				} `json:"models"`
			}
			require.NoError(t, json.Unmarshal(data, &nodes))
			require.NotEmpty(t, nodes)
			local := nodes[0].Models[tt.model].Args
			deployment := (&Broker{}).ResolveModelDeployment(types.Model{
				Id: tt.model, ModelArgs: []string{
					"--tensor-parallel-size", tt.tp, "--max-model-len", tt.maxModelLen,
				},
			}, ModelArgs{Args: local})
			require.Equal(t, tt.fingerprint, deployment.Fingerprint())
		})
	}
}

func TestResolveModelDeployment_OverrideOwnsDeploymentFlags(t *testing.T) {
	b := &Broker{}
	deployment := b.ResolveModelDeployment(types.Model{
		Id: "MiniMaxAI/MiniMax-M2.7",
		ModelArgs: []string{
			"--revision=old",
			"--rev", "another-old-revision",
			"--served-model-name", "old-alias", "second-alias",
			"--served_model_name=another-old-alias",
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

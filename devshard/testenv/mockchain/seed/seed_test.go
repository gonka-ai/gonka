package seed_test

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/mockchain/seed"

	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	st, err := seed.Load("")
	require.NoError(t, err)
	require.Equal(t, "gonka-test", st.GetChainID())
	require.NotNil(t, st.GetEscrow(1))
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(`
chain_id: custom-chain
block_height: 200
epoch:
  index: 3
  poc_start_block_height: 180
params:
  logprobs_mode: off
  devshard_requests_enabled: true
  validation_rate: 7000
participants:
  - address: gonka1abc
    inference_url: http://router:8080/devshard/v1
escrows:
  - id: 42
    creator: gonka1creator
    amount: 99
    slots: ["http://router:8080/devshard/v1"]
    epoch_index: 3
    app_hash: "aa"
    model_id: m1
grantees:
  - granter_address: gonka1abc
    message_type_url: /inference.inference.MsgStartInference
    grantees: [gonka1warm]
epoch_groups:
  - epoch_index: 3
    model_id: m1
    validation_threshold_value: 75
`), 0o644)
	require.NoError(t, err)

	st, err := seed.Load(path)
	require.NoError(t, err)
	require.Equal(t, "custom-chain", st.GetChainID())
	require.Equal(t, int64(200), st.GetBlockHeight())
	require.Equal(t, uint64(3), st.GetEpoch().Index)
	require.NotNil(t, st.GetEscrow(42))
	require.Equal(t, "gonka1abc", st.GetParticipant("gonka1abc").Address)
}

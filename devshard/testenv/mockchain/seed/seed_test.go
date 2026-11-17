package seed_test

import (
	"os"
	"path/filepath"
	"testing"

	"devshard/bridge"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/seed"

	inferencetypes "github.com/productscience/inference/x/inference/types"
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
  refusal_timeout: 5
  execution_timeout: 17
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
	escrow := st.GetEscrow(42)
	require.NotNil(t, escrow)
	require.Equal(t, int64(5), escrow.RefusalTimeout)
	require.Equal(t, int64(17), escrow.ExecutionTimeout)
	require.Equal(t, int64(5), st.GetParams().DevshardEscrowParams.RefusalTimeout)
	require.Equal(t, int64(17), st.GetParams().DevshardEscrowParams.ExecutionTimeout)
	require.Equal(t, "gonka1abc", st.GetParticipant("gonka1abc").Address)
}

func TestFromFile_OmittedTimeoutsUseDefaults(t *testing.T) {
	st, err := seed.FromFile(&config.File{
		Participants: []config.Participant{{Address: "gonka1abc"}},
		Escrows:      []config.Escrow{{ID: 42}},
	})
	require.NoError(t, err)

	params := st.GetParams().DevshardEscrowParams
	require.Equal(t, inferencetypes.DefaultDevshardRefusalTimeout, params.RefusalTimeout)
	require.Equal(t, inferencetypes.DefaultDevshardExecutionTimeout, params.ExecutionTimeout)

	escrow := st.GetEscrow(42)
	require.NotNil(t, escrow)
	require.Equal(t, params.RefusalTimeout, escrow.RefusalTimeout)
	require.Equal(t, params.ExecutionTimeout, escrow.ExecutionTimeout)
	require.False(t, params.HeightsyncTimeoutClock)
	require.False(t, escrow.HeightsyncTimeoutClock)
}

func TestFromFile_HeightsyncTimeoutClock(t *testing.T) {
	header := true
	st, err := seed.FromFile(&config.File{
		Params:  config.Params{HeightsyncTimeoutClock: &header},
		Escrows: []config.Escrow{{ID: 7}},
	})
	require.NoError(t, err)
	require.True(t, st.GetParams().DevshardEscrowParams.HeightsyncTimeoutClock)
	require.True(t, st.GetEscrow(7).HeightsyncTimeoutClock)

	legacy := false
	st, err = seed.FromFile(&config.File{
		Params: config.Params{HeightsyncTimeoutClock: &header},
		Escrows: []config.Escrow{{
			ID:                     8,
			HeightsyncTimeoutClock: &legacy,
		}},
	})
	require.NoError(t, err)
	require.True(t, st.GetParams().DevshardEscrowParams.HeightsyncTimeoutClock)
	require.False(t, st.GetEscrow(8).HeightsyncTimeoutClock, "an escrow value overrides params")
}

func TestApplyHeightsyncTimeoutClockBindsTheEscrow(t *testing.T) {
	st, err := seed.FromFile(&config.File{Escrows: []config.Escrow{{ID: 1, AppHash: "aa"}}})
	require.NoError(t, err)
	require.False(t, st.GetEscrow(1).HeightsyncTimeoutClock)

	seed.ApplyHeightsyncTimeoutClock(st, true)
	escrow := st.GetEscrow(1)
	require.True(t, escrow.HeightsyncTimeoutClock)
	require.True(t, st.GetParams().DevshardEscrowParams.HeightsyncTimeoutClock)

	info, err := bridge.EscrowInfoFromQuery(1, escrow)
	require.NoError(t, err)
	cfg := bridge.SessionConfigAtBind(3, info)
	require.True(t, cfg.HeightsyncTimeoutClock, "the session binds the escrow flag, not a process-only override")
}

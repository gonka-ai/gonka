package v0_2_18

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/require"
)

// Release gate (plan Step 1, finding M3): the upgrade handler halts the chain
// if CometBFT's own ValidateUpdate/ValidateBasic reject the live params, so the
// enable logic must be exercised against what mainnet actually stores.

// Live mainnet consensus params, read from
// http://node1.gonka.ai:8000/chain-rpc/consensus_params at height 6971588
// (2026-11-09). Identical to genesis/genesis.json. Re-check before tagging
// v0.2.18 with: GONKA_LIVE_RPC=<rpc> go test ./app/upgrades/v0_2_18/ -run LiveRPC
func mainnetParams20261109() cmtproto.ConsensusParams {
	return cmtproto.ConsensusParams{
		Block:     &cmtproto.BlockParams{MaxBytes: 22020096, MaxGas: -1},
		Evidence:  &cmtproto.EvidenceParams{MaxAgeNumBlocks: 100000, MaxAgeDuration: 172800 * time.Second, MaxBytes: 1048576},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: []string{"ed25519"}},
		Version:   &cmtproto.VersionParams{App: 0},
		Abci:      &cmtproto.ABCIParams{VoteExtensionsEnableHeight: 0},
	}
}

func TestEnableVoteExtensions_MainnetParamsVector(t *testing.T) {
	const height = 6971588
	store := &memParamsStore{params: mainnetParams20261109()}
	enableHeight, changed, err := EnableVoteExtensions(ctxAt(height), store, VoteExtensionsEnableDelay)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(height)+VoteExtensionsEnableDelay, enableHeight)
	// Only the ABCI section changes.
	got := store.params
	want := mainnetParams20261109()
	require.Equal(t, want.Block, got.Block)
	require.Equal(t, want.Evidence, got.Evidence)
	require.Equal(t, want.Validator, got.Validator)
	require.Equal(t, want.Version, got.Version)
}

// TestEnableVoteExtensions_LiveRPC runs the enable logic against the params a
// live node reports. Skipped unless GONKA_LIVE_RPC is set (no network in CI).
func TestEnableVoteExtensions_LiveRPC(t *testing.T) {
	rpc := strings.TrimRight(os.Getenv("GONKA_LIVE_RPC"), "/")
	if rpc == "" {
		t.Skip("set GONKA_LIVE_RPC (e.g. http://node1.gonka.ai:8000/chain-rpc) to check live consensus params")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rpc + "/consensus_params")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	var res coretypes.ResultConsensusParams
	require.NoError(t, cmtjson.Unmarshal(envelope.Result, &res))

	live := res.ConsensusParams.ToProto()
	t.Logf("live params at height %d: %+v", res.BlockHeight, live)
	store := &memParamsStore{params: live}
	enableHeight, changed, err := EnableVoteExtensions(ctxAt(res.BlockHeight), store, VoteExtensionsEnableDelay)
	require.NoError(t, err, "the v0.2.18 handler would fail (and halt the chain) on these params")
	if changed {
		require.Equal(t, res.BlockHeight+VoteExtensionsEnableDelay, enableHeight)
	} else {
		t.Logf("vote extensions already scheduled at %d; handler leaves params unchanged", enableHeight)
	}
}

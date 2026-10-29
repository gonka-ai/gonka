package hostevents

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/bridge"

	abci "github.com/cometbft/cometbft/abci/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

func TestApplyWarm_CometDebugDAPIInfo(t *testing.T) {
	var logs bytes.Buffer
	warm := bridge.NewWarmKeyLogCopy(nil, time.Hour)
	p := NewPoller(PollerConfig{
		Config: Config{Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))},
		Warm:   warm,
	})
	replace := ctypes.ResultEvent{Data: cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: 10,
		Result: abci.ExecTxResult{Events: []abci.Event{
			logWarmABCI("warm_key_revoked", "cold", "old", "1", "10", "WARM_KEY_LOG_END_REVOKED"),
			logWarmABCI("warm_key_granted", "cold", "new", "2", "10", ""),
		}},
	}}}
	// Each warm-key query delivers the same transaction.
	for range WarmKeyQueries() {
		p.OfferCometWarm(WarmKeysFromResult(replace))
	}
	_, ok := p.dispatch([]*gen.HostEvent{
		{
			Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
			WarmKey: &gen.WarmKeyPayload{
				Granter: "cold", Grantee: "old", GrantId: 1, Height: 10, End: "WARM_KEY_LOG_END_REVOKED",
			},
		},
		{
			Kind: gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
			WarmKey: &gen.WarmKeyPayload{
				Granter: "cold", Grantee: "new", GrantId: 2, Height: 10,
			},
		},
	})
	require.True(t, ok)

	ended, found, err := warm.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended())
	live, err := warm.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Equal(t, []bridge.WarmKeyEntry{{
		Granter: "cold", ID: 2, Grantee: "new", GrantedAtHeight: 10,
	}}, live)

	info, debug := 0, 0
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "hostevents: warm-key") {
			continue
		}
		switch {
		case strings.Contains(line, "level=INFO"):
			info++
		case strings.Contains(line, "level=DEBUG"):
			debug++
		}
	}
	require.Equal(t, 2, info, "info is the dapi revoke and the dapi grant")
	require.Equal(t, 4, debug, "each query handler logs the revoke and the grant at debug")
}

func logWarmABCI(typ, granter, grantee, id, height, end string) abci.Event {
	attrs := []abci.EventAttribute{
		{Key: "granter", Value: granter},
		{Key: "grantee", Value: grantee},
		{Key: "grant_id", Value: id},
		{Key: "height", Value: height},
	}
	if end != "" {
		attrs = append(attrs, abci.EventAttribute{Key: "end", Value: end})
	}
	return abci.Event{Type: typ, Attributes: attrs}
}

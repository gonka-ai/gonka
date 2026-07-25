package nodemanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"common/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNodeManager_GetRuntimeConfig_BackCompat_LegacyClientImmediateUnchanged(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	// 3a client: only client_params_block_height, no max_wait_seconds field.
	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.True(t, resp.Unchanged)
	require.Nil(t, resp.Config)
}

func TestNodeManager_GetRuntimeConfig_BackCompat_LegacyClientImmediateFull(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 50,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.False(t, resp.Unchanged)
	require.Equal(t, int64(100), resp.Config.ParamsBlockHeight)
}

func TestNodeManager_GetRuntimeConfig_BackCompat_ExplicitZeroEqualsLegacy(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	legacy, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
	})
	require.NoError(t, err)

	explicit, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          0,
	})
	require.NoError(t, err)
	require.Equal(t, legacy.Unchanged, explicit.Unchanged)
	require.Nil(t, explicit.Config)
}

func TestNodeManager_GetRuntimeConfig_BackCompat_NegativeMaxWaitTreatedAsZero(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 100,
		MaxWaitSeconds:          -1,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.True(t, resp.Unchanged)
}

func TestNodeManager_GetRuntimeConfig_BackCompat_WireFormat_LegacyBytesDecode(t *testing.T) {
	// Wire bytes for field 1 only (client_params_block_height=100), as a 3a client would send.
	legacyBytes := []byte{0x08, 0x64} // tag 1, varint 100

	req := &gen.GetRuntimeConfigRequest{}
	require.NoError(t, proto.Unmarshal(legacyBytes, req))
	require.Equal(t, int64(100), req.ClientParamsBlockHeight)
	require.Equal(t, int32(0), req.MaxWaitSeconds)

	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	start := time.Now()
	resp, err := srv.GetRuntimeConfig(context.Background(), req)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.True(t, resp.Unchanged)
}

func TestNodeManager_GetRuntimeConfig_BackCompat_HeterogeneousFleet(t *testing.T) {
	cm := testConfigManager(t)
	populateRuntimeConfig(t, cm, 100, 0)
	srv := NewServer(&mockBroker{}, cm, testPhaseTrackerWithEpoch(1))

	var legacyWg sync.WaitGroup
	legacyWg.Add(2)
	for range 2 {
		go func() {
			defer legacyWg.Done()
			start := time.Now()
			resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
				ClientParamsBlockHeight: 100,
			})
			require.NoError(t, err)
			require.Less(t, time.Since(start), 50*time.Millisecond)
			require.True(t, resp.Unchanged)
		}()
	}
	legacyWg.Wait()

	longResults := make(chan *gen.GetRuntimeConfigResponse, 2)
	for range 2 {
		go func() {
			resp, err := srv.GetRuntimeConfig(context.Background(), &gen.GetRuntimeConfigRequest{
				ClientParamsBlockHeight: 100,
				MaxWaitSeconds:          5,
			})
			require.NoError(t, err)
			longResults <- resp
		}()
	}

	time.Sleep(100 * time.Millisecond)
	select {
	case <-longResults:
		t.Fatal("long-poll should still be blocked before notify")
	default:
	}

	require.NoError(t, cm.SetValidationParams(apiconfig.ValidationParamsCache{LogprobsMode: "raw"}))
	require.True(t, cm.ApplyRuntimeConfigBlockIfChanged(101, 0))

	for i := 0; i < 2; i++ {
		select {
		case resp := <-longResults:
			require.False(t, resp.Unchanged)
			require.Equal(t, int64(101), resp.Config.ParamsBlockHeight)
		case <-time.After(2 * time.Second):
			t.Fatalf("long-poll waiter %d did not wake", i)
		}
	}
}

func TestNodeManager_GetRuntimeConfig_BackCompat_AcquireMLNodeUnchanged(t *testing.T) {
	// Capture wire encoding stability for unrelated messages when extending GetRuntimeConfigRequest.
	acquireReq := &gen.AcquireMLNodeRequest{Model: "gpt4", ExcludedNodes: []string{"n1"}}
	acquireBytes, err := proto.Marshal(acquireReq)
	require.NoError(t, err)

	decoded := &gen.AcquireMLNodeRequest{}
	require.NoError(t, proto.Unmarshal(acquireBytes, decoded))
	require.Equal(t, acquireReq.Model, decoded.Model)
	require.Equal(t, acquireReq.ExcludedNodes, decoded.ExcludedNodes)

	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, model string, skip []string) (string, string, string, error) {
			require.Equal(t, "gpt4", model)
			require.Equal(t, []string{"n1"}, skip)
			return "lock", "http://ep", "node", nil
		},
	}, testConfigManager(t), nil)
	resp, err := srv.AcquireMLNode(context.Background(), decoded)
	require.NoError(t, err)
	require.Equal(t, "lock", resp.LockId)
}

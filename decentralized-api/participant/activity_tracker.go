package participant

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync/atomic"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// ActivityTracker caches whether the current participant is part of
// the active epoch group. The result is refreshed periodically by a
// background goroutine so that admin HTTP handlers can call
// IsActive() without making any chain RPC calls per request.
//
// "Active" means the participant has at least one MLnode assigned to
// some model subgroup in the current epoch group data.
type ActivityTracker struct {
	queryClient queryClient
	address     string
	interval    time.Duration

	active atomic.Bool
}

// queryClient is a narrow interface over cosmosclient.CosmosMessageClient
// covering only what the tracker needs. Defined to make unit testing
// straightforward.
type queryClient interface {
	NewInferenceQueryClient() types.QueryClient
}

// NewActivityTracker constructs a tracker but does not start it. Call
// Start to launch the background refresher.
func NewActivityTracker(client cosmosclient.CosmosMessageClient, address string, interval time.Duration) *ActivityTracker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &ActivityTracker{
		queryClient: client,
		address:     address,
		interval:    interval,
	}
}

// IsActive returns the most recently observed activity status. False
// until the first successful refresh completes.
func (t *ActivityTracker) IsActive() bool {
	return t.active.Load()
}

// Start launches a goroutine that refreshes IsActive() immediately and
// then every interval until ctx is cancelled. Start returns without
// blocking: the initial refresh runs inside the goroutine so a slow or
// unresponsive chain RPC can't hold up API startup (IsActive() stays
// false until the first successful refresh).
func (t *ActivityTracker) Start(ctx context.Context) {
	go func() {
		t.refresh(ctx)
		ticker := time.NewTicker(t.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.refresh(ctx)
			}
		}
	}()
}

func (t *ActivityTracker) refresh(ctx context.Context) {
	active, err := t.query(ctx)
	if err != nil {
		// Chain may not be ready at startup; keep the previous value
		// rather than flipping to false on transient errors.
		logging.Debug("ActivityTracker refresh failed", types.Participants, "error", err)
		return
	}
	if t.active.Swap(active) != active {
		logging.Info("Participant active status changed", types.Participants, "active", active)
	}
}

func (t *ActivityTracker) query(ctx context.Context) (bool, error) {
	q := t.queryClient.NewInferenceQueryClient()
	parentResp, err := q.CurrentEpochGroupData(ctx, &types.QueryCurrentEpochGroupDataRequest{})
	if err != nil {
		return false, err
	}
	if parentResp == nil {
		return false, nil
	}
	epochIndex := parentResp.EpochGroupData.EpochIndex
	for _, modelId := range parentResp.EpochGroupData.SubGroupModels {
		subResp, err := q.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
			EpochIndex: epochIndex,
			ModelId:    modelId,
		})
		if err != nil {
			// Transient RPC failure: surface the error so refresh keeps the
			// previous value instead of flipping an active participant to
			// inactive on a missing subgroup.
			return false, err
		}
		if subResp == nil {
			continue
		}
		for _, w := range subResp.EpochGroupData.ValidationWeights {
			if w.MemberAddress == t.address && len(w.MlNodes) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

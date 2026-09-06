package participant

import (
	"common/logging"
	"context"
	"decentralized-api/cosmosclient"
	"sync/atomic"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// activityState is the tri-state cached by ActivityTracker. It starts
// Unknown and only becomes Active/Inactive once a refresh succeeds, so
// callers can distinguish "not yet known" from "known inactive".
type activityState int32

const (
	activityUnknown activityState = iota
	activityInactive
	activityActive
)

// defaultRefreshTimeout bounds a single chain query so a hung RPC cannot
// wedge the refresher goroutine (which would otherwise leave the cached
// value unboundedly stale).
const defaultRefreshTimeout = 5 * time.Second

// ActivityTracker caches whether the current participant is part of
// the active epoch group. The result is refreshed periodically by a
// background goroutine so that admin HTTP handlers can call
// IsActive() without making any chain RPC calls per request.
//
// "Active" means the participant has at least one MLnode assigned to
// some model subgroup in the current epoch group data.
type ActivityTracker struct {
	queryClient    queryClient
	address        string
	interval       time.Duration
	refreshTimeout time.Duration

	state atomic.Int32
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
		queryClient:    client,
		address:        address,
		interval:       interval,
		refreshTimeout: defaultRefreshTimeout,
	}
}

// IsActive returns true only when the most recent successful refresh saw
// the participant in the active set. It is false both for "known inactive"
// and for "not yet known"; use IsKnown to tell those apart.
func (t *ActivityTracker) IsActive() bool {
	return activityState(t.state.Load()) == activityActive
}

// IsKnown reports whether at least one refresh has succeeded, i.e. whether
// IsActive reflects a real observation rather than the startup default.
func (t *ActivityTracker) IsKnown() bool {
	return activityState(t.state.Load()) != activityUnknown
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
	// Bound each refresh so a hung-but-not-erroring RPC cannot block the
	// refresher loop forever (which would leave the cached value stale and
	// stop all future ticks from being processed).
	timeout := t.refreshTimeout
	if timeout <= 0 {
		timeout = defaultRefreshTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	active, err := t.query(queryCtx)
	if err != nil {
		// Chain may not be ready at startup; keep the previous value
		// (including Unknown) rather than flipping it on transient errors.
		logging.Debug("ActivityTracker refresh failed", types.Participants, "error", err)
		return
	}
	newState := activityInactive
	if active {
		newState = activityActive
	}
	if activityState(t.state.Swap(int32(newState))) != newState {
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

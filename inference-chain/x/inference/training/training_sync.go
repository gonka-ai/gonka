package membership

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type MembershipRecord struct {
	LastHeartbeat time.Time
	Rank          int
}

type RunState struct {
	LastEpoch          int
	LastEpochTimestamp time.Time
	FinishedEpochs     map[int]bool
}

// EpochState holds per‑epoch membership info.
type EpochState struct {
	Records map[string]*MembershipRecord
}

type TrainingRunStore interface {
	GetRunState(ctx context.Context, runId string) (*RunState, error)
	SaveRunState(ctx context.Context, runId string, state *RunState) error

	GetEpochState(ctx context.Context, runId string, epoch int) (*EpochState, error)
	SaveEpochState(ctx context.Context, runId string, epoch int, state *EpochState) error
}

// RunMembershipService is the public API.
type RunMembershipService interface {
	Join(ctx context.Context, nodeID string, epoch int) error
	Heartbeat(ctx context.Context, nodeID string, epoch int) error
	GetEpochActiveNodes(ctx context.Context, epoch int) ([]string, error)
	AssignRank(ctx context.Context) error
	FinishIfNeeded(ctx context.Context) error
	RerankIfSomeNodesLeft(ctx context.Context, epoch int) error
}

type RunMembership struct {
	mu               sync.Mutex
	runId            string
	store            TrainingRunStore
	minNodes         int
	maxNodes         int
	joinTimeout      time.Duration
	heartbeatTimeout time.Duration
}

func NewRunMembership(
	runId string,
	store TrainingRunStore,
	minNodes, maxNodes int,
	joinTimeout, heartbeatTimeout time.Duration,
) *RunMembership {
	return &RunMembership{
		runId:            runId,
		store:            store,
		minNodes:         minNodes,
		maxNodes:         maxNodes,
		joinTimeout:      joinTimeout,
		heartbeatTimeout: heartbeatTimeout,
	}
}

func (rm *RunMembership) Join(ctx context.Context, nodeID string, epoch int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// --- load or init run state ---
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	if rs == nil {
		rs = &RunState{
			LastEpoch:      -1,
			FinishedEpochs: make(map[int]bool),
		}
	}

	// epoch sanity checks
	if epoch < rs.LastEpoch {
		return fmt.Errorf("joining outdated epoch %d, last is %d", epoch, rs.LastEpoch)
	}
	if epoch == rs.LastEpoch && rs.FinishedEpochs[epoch] {
		return fmt.Errorf("joining epoch %d after finish", epoch)
	}

	// new epoch: reset timestamp
	if epoch > rs.LastEpoch {
		rs.LastEpoch = epoch
		rs.LastEpochTimestamp = time.Now()
		if err := rm.store.SaveRunState(ctx, rm.runId, rs); err != nil {
			return err
		}
	}

	// --- upsert record in epoch state ---
	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	if es == nil {
		es = &EpochState{Records: make(map[string]*MembershipRecord)}
	}
	es.Records[nodeID] = &MembershipRecord{
		LastHeartbeat: time.Now(),
		Rank:          -1,
	}
	if err := rm.store.SaveEpochState(ctx, rm.runId, epoch, es); err != nil {
		return err
	}

	// maybe finish this epoch
	return rm.finishIfNeededNoLock(ctx)
}

func (rm *RunMembership) Heartbeat(ctx context.Context, nodeID string, epoch int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	rec, ok := es.Records[nodeID]
	if !ok {
		return fmt.Errorf("node %s not joined in epoch %d", nodeID, epoch)
	}
	rec.LastHeartbeat = time.Now()
	if err := rm.store.SaveEpochState(ctx, rm.runId, epoch, es); err != nil {
		return err
	}

	return rm.finishIfNeededNoLock(ctx)
}

// GetEpochActiveNodes returns all nodes with heartbeat within heartbeatTimeout.
func (rm *RunMembership) GetEpochActiveNodes(ctx context.Context, epoch int) ([]string, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var active []string
	for id, rec := range es.Records {
		if now.Sub(rec.LastHeartbeat) <= rm.heartbeatTimeout {
			active = append(active, id)
		}
	}
	sort.Strings(active)
	return active, nil
}

// AssignRank assigns ranks 0..N-1 to all active nodes in the current epoch.
func (rm *RunMembership) AssignRank(ctx context.Context) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// load run state
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	epoch := rs.LastEpoch

	active, err := rm.GetEpochActiveNodes(ctx, epoch)
	if err != nil {
		return err
	}
	if len(active) < rm.minNodes || len(active) > rm.maxNodes {
		return fmt.Errorf("cannot assign rank: active=%d, want [%d,%d]",
			len(active), rm.minNodes, rm.maxNodes)
	}

	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	for rank, nodeID := range active {
		es.Records[nodeID].Rank = rank
	}
	rs.FinishedEpochs[epoch] = true

	if err := rm.store.SaveEpochState(ctx, rm.runId, epoch, es); err != nil {
		return err
	}
	return rm.store.SaveRunState(ctx, rm.runId, rs)
}

// FinishIfNeeded is the exported version of finishIfNeededNoLock.
func (rm *RunMembership) FinishIfNeeded(ctx context.Context) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.finishIfNeededNoLock(ctx)
}

// finishIfNeededNoLock checks join/timeout conditions and assigns rank if ready.
// **Caller must hold rm.mu.**
func (rm *RunMembership) finishIfNeededNoLock(ctx context.Context) error {
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	epoch := rs.LastEpoch

	active, err := rm.GetEpochActiveNodes(ctx, epoch)
	if err != nil {
		return err
	}
	joined := len(active)
	now := time.Now()
	enough := joined == rm.maxNodes
	enoughTimeout := joined >= rm.minNodes && now.Sub(rs.LastEpochTimestamp) > rm.joinTimeout

	if !(enough || enoughTimeout) {
		return nil
	}
	// reuse AssignRank (which also marks FinishedEpochs)
	return rm.AssignRank(ctx)
}

// RerankIfSomeNodesLeft is now exported.
func (rm *RunMembership) RerankIfSomeNodesLeft(ctx context.Context, epoch int) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.rerankIfSomeNodesLeftNoLock(ctx, epoch)
}

// rerankIfSomeNodesLeftNoLock handles re‑ranking when nodes drop out.
// **Caller must hold rm.mu.**
func (rm *RunMembership) rerankIfSomeNodesLeftNoLock(ctx context.Context, epoch int) error {
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	if !rs.FinishedEpochs[epoch] {
		return fmt.Errorf("epoch %d not finished", epoch)
	}

	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	// collect originally ranked nodes
	var original []string
	for id, rec := range es.Records {
		if rec.Rank != -1 {
			original = append(original, id)
		}
	}
	sort.Strings(original)

	// collect still‑alive
	aliveMap := map[string]bool{}
	active, err := rm.GetEpochActiveNodes(ctx, epoch)
	if err != nil {
		return err
	}
	for _, id := range active {
		aliveMap[id] = true
	}

	// if some dropped, reassign among survivors
	var survivors []string
	for _, id := range original {
		if aliveMap[id] {
			survivors = append(survivors, id)
		}
	}
	if len(survivors) < len(original) {
		for rank, nodeID := range survivors {
			es.Records[nodeID].Rank = rank
		}
		for _, nodeID := range original {
			if !aliveMap[nodeID] {
				es.Records[nodeID].Rank = -1
			}
		}
		return rm.store.SaveEpochState(ctx, rm.runId, epoch, es)
	}
	return nil
}

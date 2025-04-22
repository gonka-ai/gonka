package training

import (
	"context"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"sort"
	"time"
)

type MembershipRecord struct {
	LastHeartbeat time.Time
	Rank          int
}

// TODO: delet this
type RunState struct {
	LastEpoch          int
	LastEpochTimestamp time.Time
	FinishedEpochs     map[int]bool
}

// EpochState holds per‑epoch membership info.
type EpochState struct {
	Epoch    int32
	Activity []*types.TrainingTaskNodeEpochActivity
}

type RunStore interface {
	GetRunState(ctx context.Context, runId uint64) (*types.TrainingTask, error)
	SaveRunState(ctx context.Context, state *types.TrainingTask) error

	GetEpochState(ctx context.Context, runId uint64, epoch int32) (*EpochState, error)
	SaveEpochState(ctx context.Context, runId uint64, epoch int32, state *EpochState) error
	GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, participant string, nodeId string) (*types.TrainingTaskNodeEpochActivity, error)
	SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity) error
}

// RunMembershipService is the public API.
type RunMembershipService interface {
	Join(ctx context.Context, nodeId string, epoch int32, participant string) error
	Heartbeat(ctx context.Context, nodeId string, epoch int32) error
	GetEpochActiveNodes(ctx context.Context, epoch int32) ([]string, error)
	AssignRank(ctx context.Context) error
	FinishIfNeeded(ctx context.Context) error
	RerankIfSomeNodesLeft(ctx context.Context, epoch int32) error
}

type RunManager struct {
	runId            uint64
	store            RunStore
	minNodes         int
	maxNodes         int
	joinTimeout      time.Duration
	heartbeatTimeout time.Duration
}

const (
	defaultJoinTimeout      = 5 * time.Minute
	defaultHeartbeatTimeout = 5 * time.Minute
)

func NewRunManager(
	runId uint64,
	store RunStore,
	minNodes, maxNodes int,
) *RunManager {
	return &RunManager{
		runId:            runId,
		store:            store,
		minNodes:         minNodes,
		maxNodes:         maxNodes,
		joinTimeout:      defaultJoinTimeout,
		heartbeatTimeout: defaultHeartbeatTimeout,
	}
}

type BlockInfo struct {
	height    int64
	timestamp time.Time
}

func NewBlockInfo(ctx sdk.Context) BlockInfo {
	return BlockInfo{
		height:    ctx.BlockHeight(),
		timestamp: ctx.BlockTime(),
	}
}

func (rm *RunManager) Join(ctx context.Context, nodeId string, epoch int32, blockInfo BlockInfo, participant string) error {
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	if rs == nil {
		return fmt.Errorf("run %d not found", rm.runId)
	}

	lastEpoch := rs.Epoch.LastEpoch
	if epoch < 0 {
		return fmt.Errorf("bad request. invalid epoch %d", epoch)
	}
	if epoch < lastEpoch {
		return fmt.Errorf("joining outdated epoch %d, last is %d", epoch, lastEpoch)
	}
	if epoch == lastEpoch && rs.Epoch.LastEpochIsFinished {
		return fmt.Errorf("joining epoch %d after finish", epoch)
	}

	// new epoch
	if epoch > lastEpoch {
		rs.Epoch.LastEpoch = epoch
		rs.Epoch.LastEpochIsFinished = false
		rs.Epoch.LastEpochBlockHeight = blockInfo.height
		rs.Epoch.LastEpochTimestamp = blockInfo.timestamp.Unix()

		if err := rm.store.SaveRunState(ctx, rs); err != nil {
			return err
		}
	}

	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	if es == nil {
		es = &EpochState{
			Epoch: epoch,
			Activity: []*types.TrainingTaskNodeEpochActivity{
				{
					TaskId:      rm.runId,
					Participant: participant,
					NodeId:      nodeId,
					Epoch:       epoch,
					BlockHeight: blockInfo.height,
					BlockTime:   blockInfo.timestamp.Unix(),
				},
			},
		}
	} else {
		// TODO: modify existing of smth
	}

	if err := rm.store.SaveEpochState(ctx, rm.runId, epoch, es); err != nil {
		return err
	}

	return rm.FinishIfNeeded(ctx)
}

func (rm *RunManager) Heartbeat(ctx context.Context, nodeId string, epoch int32, blockInfo BlockInfo) error {
	activity, err := rm.store.GetParticipantActivity(ctx, rm.runId, epoch, "", nodeId)
	if err != nil {
		// PRTODO: find a way to log errors here
		// So here it probably means not joined in epoch or smth
		return err
	}

	activity.BlockTime = blockInfo.height
	activity.BlockTime = blockInfo.timestamp.UnixMilli() // PRTODO: FIXME: think of a way for converting timestamps

	if err := rm.store.SaveParticipantActivity(ctx, activity); err != nil {
		return err
	}

	return rm.FinishIfNeeded(ctx)
}

// GetEpochActiveNodes returns all nodes with heartbeat within heartbeatTimeout.
func (rm *RunManager) GetEpochActiveNodes(ctx context.Context, epoch int32) ([]string, error) {
	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var active []string
	for id, rec := range es.Activity {
		if now.Sub(rec.LastHeartbeat) <= rm.heartbeatTimeout {
			active = append(active, id)
		}
	}
	sort.Strings(active)
	return active, nil
}

// AssignRank assigns ranks 0...N-1 to all active nodes in the current epoch.
func (rm *RunManager) AssignRank(ctx context.Context) error {
	// load run state
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	epoch := rs.Epoch.LastEpoch

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
		es.Activity[nodeID].Rank = rank
	}
	rs.Epoch.LastEpochIsFinished = true

	if err := rm.store.SaveEpochState(ctx, rm.runId, epoch, es); err != nil {
		return err
	}
	return rm.store.SaveRunState(ctx, rs)
}

// FinishIfNeeded is the exported version of finishIfNeededNoLock.
func (rm *RunManager) FinishIfNeeded(ctx context.Context) error {
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}
	epoch := rs.Epoch.LastEpoch

	active, err := rm.GetEpochActiveNodes(ctx, epoch)
	if err != nil {
		return err
	}
	joined := len(active)
	now := time.Now()
	enough := joined == rm.maxNodes
	// PRTODO: FIXME: don't even use now or time
	enoughTimeout := joined >= rm.minNodes && now.Sub(time.UnixMilli(rs.Epoch.LastEpochTimestamp)) > rm.joinTimeout

	if !(enough || enoughTimeout) {
		return nil
	}
	// reuse AssignRank (which also marks FinishedEpochs)
	return rm.AssignRank(ctx)
}

func (rm *RunManager) rerankIfSomeNodesLeft(ctx context.Context, epoch int32) error {
	rs, err := rm.store.GetRunState(ctx, rm.runId)
	if err != nil {
		return err
	}

	if epoch == rs.Epoch.LastEpoch && !rs.Epoch.LastEpochIsFinished {
		return fmt.Errorf("epoch %d not finished", epoch)
	} else if epoch > rs.Epoch.LastEpoch {
		return fmt.Errorf("Unexpected epoch received in rerank not finished. epoch = %d. lastEpoch = %d", epoch, rs.Epoch.LastEpoch)
	} else if epoch < rs.Epoch.LastEpoch {
		// TODO: log epoch
	}

	es, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	// collect originally ranked nodes
	var original []string
	for id, rec := range es.Activity {
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
			es.Activity[nodeID].Rank = rank
		}
		for _, nodeID := range original {
			if !aliveMap[nodeID] {
				es.Activity[nodeID].Rank = -1
			}
		}
		return rm.store.SaveEpochState(ctx, rm.runId, epoch, es)
	}
	return nil
}

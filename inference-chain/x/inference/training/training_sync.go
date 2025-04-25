package training

import (
	"context"
	"fmt"
	"sort"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// EpochState holds per‑epoch membership info.
type EpochState struct {
	Epoch    int32
	Activity map[NodeId]*types.TrainingTaskNodeEpochActivity
}

func NewEpochState(activity []*types.TrainingTaskNodeEpochActivity) (*EpochState, error) {
	if len(activity) == 0 {
		return nil, fmt.Errorf("empty activity")
	}

	epoch := activity[0].Epoch
	activityMap := make(map[NodeId]*types.TrainingTaskNodeEpochActivity, len(activity))
	for _, rec := range activity {
		if epoch != rec.Epoch {
			return nil, fmt.Errorf("epoch does not match epoch %d", epoch)
		}
		key := NodeId{
			Participant: rec.Participant,
			NodeId:      rec.NodeId,
		}
		activityMap[key] = rec
	}

	return &EpochState{
		Epoch:    activity[0].Epoch,
		Activity: activityMap,
	}, nil
}

func (es *EpochState) toActivityArray() []*types.TrainingTaskNodeEpochActivity {
	activity := make([]*types.TrainingTaskNodeEpochActivity, 0, len(es.Activity))
	for _, rec := range es.Activity {
		activity = append(activity, rec)
	}
	sort.Slice(activity, func(i, j int) bool {
		if activity[i].Participant != activity[j].Participant {
			return activity[i].Participant < activity[j].Participant
		}
		return activity[i].NodeId < activity[j].NodeId
	})
	return activity
}

type RunStore interface {
	GetRunState(ctx context.Context, runId uint64) *types.TrainingTask
	SaveRunState(ctx context.Context, state *types.TrainingTask) error

	GetEpochState(ctx context.Context, runId uint64, epoch int32) ([]*types.TrainingTaskNodeEpochActivity, error)
	SaveEpochState(ctx context.Context, state []*types.TrainingTaskNodeEpochActivity) error

	GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, participant string, nodeId string) *types.TrainingTaskNodeEpochActivity
	SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity)

	SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier)
	GetBarrierEpochStatus(ctx context.Context, key types.TrainingTaskBarrierEpochKey) ([]*types.TrainingTaskBarrier, error)
}

// RunMembershipService is the public API.
type RunMembershipService interface {
	Join(ctx context.Context, nodeId string, epoch int32, block BlockInfo, participant string) error
	Heartbeat(ctx context.Context, participant string, nodeId string, epoch int32, block BlockInfo) error
	GetEpochActiveNodes(ctx context.Context, epoch int32, block BlockInfo) ([]NodeId, error)
	AssignRank(ctx context.Context, block BlockInfo) error
	FinishIfNeeded(ctx context.Context, block BlockInfo) error
	RerankIfSomeNodesLeft(ctx context.Context, epoch int32, block BlockInfo) error
	SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier, block BlockInfo) error
	GetBarrierStatus(ctx context.Context, req *types.GetBarrierStatusRequest) (*types.GetBarrierStatusResponse, error)
}

type NodeId struct {
	Participant string
	NodeId      string
}

type RunManager struct {
	runId            uint64
	store            RunStore
	minNodes         int
	maxNodes         int
	joinTimeout      int64
	heartbeatTimeout int64
}

// FIXME: should we use blocks or time?
const (
	defaultMinNodes         = 3
	defaultMaxNodes         = 3
	defaultJoinTimeout      = 30 // 30 blocks
	defaultHeartbeatTimeout = 30 // 30 blocks
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

// NewBlockInfoFromValues creates a BlockInfo for testing purposes.
func NewBlockInfoFromValues(height int64, timestamp time.Time) BlockInfo {
	return BlockInfo{
		height:    height,
		timestamp: timestamp,
	}
}

func NewBlockInfo(ctx sdk.Context) BlockInfo {
	return BlockInfo{
		height:    ctx.BlockHeight(),
		timestamp: ctx.BlockTime(),
	}
}

func (bi BlockInfo) Height() int64 {
	return bi.height
}

func (bi BlockInfo) Timestamp() time.Time {
	return bi.timestamp
}

// Helper function to sort NodeId slices deterministically
func sortNodeIds(nodeIds []NodeId) {
	sort.Slice(nodeIds, func(i, j int) bool {
		if nodeIds[i].Participant != nodeIds[j].Participant {
			return nodeIds[i].Participant < nodeIds[j].Participant
		}
		return nodeIds[i].NodeId < nodeIds[j].NodeId
	})
}

func (rm *RunManager) Join(ctx sdk.Context, nodeId string, epoch int32, block BlockInfo, participant string) error {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return fmt.Errorf("run not found. task_id = %d", rm.runId)
	}

	lastEpoch := rs.Epoch.LastEpoch
	if epoch < -1 {
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
		rs.Epoch.LastEpochBlockHeight = block.height
		rs.Epoch.LastEpochTimestamp = block.timestamp.Unix()

		if err := rm.store.SaveRunState(ctx, rs); err != nil {
			return err
		}
	}

	activity := rm.getOrCreateActivityEntry(ctx, participant, nodeId, epoch)
	activity.BlockTime = block.timestamp.Unix()
	activity.BlockHeight = block.height
	rm.store.SaveParticipantActivity(ctx, &activity)

	return rm.FinishIfNeeded(ctx, block)
}

func (rm *RunManager) getOrCreateActivityEntry(ctx context.Context, participant string, nodeId string, epoch int32) types.TrainingTaskNodeEpochActivity {
	activity := rm.store.GetParticipantActivity(ctx, rm.runId, epoch, participant, nodeId)
	if activity == nil {
		activity = &types.TrainingTaskNodeEpochActivity{
			TaskId:      rm.runId,
			Participant: participant,
			NodeId:      nodeId,
			Epoch:       epoch,
			BlockHeight: 0,
			BlockTime:   0,
			Rank:        -1, // TODO: are we sure -1?
		}
	}
	return *activity
}

func (rm *RunManager) Heartbeat(ctx sdk.Context, participant string, nodeId string, epoch int32, block BlockInfo) error {
	activity := rm.getOrCreateActivityEntry(ctx, participant, nodeId, epoch)
	activity.BlockHeight = block.height
	activity.BlockTime = block.timestamp.Unix()

	rm.store.SaveParticipantActivity(ctx, &activity)

	return rm.FinishIfNeeded(ctx, block)
}

func (rm *RunManager) GetEpochActiveNodes(ctx context.Context, epoch int32, currentBlock BlockInfo) ([]NodeId, error) {
	activity, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return nil, err
	}
	es, err := NewEpochState(activity)
	if err != nil {
		return nil, err
	}

	var active []NodeId
	for nodeId, rec := range es.Activity {
		blockDiff := currentBlock.height - rec.BlockHeight
		if blockDiff <= rm.heartbeatTimeout {
			active = append(active, nodeId)
		}
	}
	sortNodeIds(active)
	return active, nil
}

func (rm *RunManager) AssignRank(ctx context.Context, block BlockInfo) error {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return fmt.Errorf("run not found. task_id = %d", rm.runId)
	}
	epoch := rs.Epoch.LastEpoch

	active, err := rm.GetEpochActiveNodes(ctx, epoch, block)
	if err != nil {
		return err
	}
	if len(active) < rm.minNodes || len(active) > rm.maxNodes {
		return fmt.Errorf("cannot assign rank: active=%d, want [%d,%d]",
			len(active), rm.minNodes, rm.maxNodes)
	}

	activity, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	es, err := NewEpochState(activity)
	if err != nil {
		return err
	}

	// PRTODO: FIXME: insepct this, fix sorting
	for i, nodeId := range active {
		key := NodeId{
			Participant: nodeId.Participant,
			NodeId:      nodeId.NodeId,
		}
		es.Activity[key].Rank = int32(i)
	}
	rs.Epoch.LastEpochIsFinished = true

	if err := rm.store.SaveEpochState(ctx, es.toActivityArray()); err != nil {
		return err
	}
	return rm.store.SaveRunState(ctx, rs)
}

// FinishIfNeeded is the exported version of finishIfNeededNoLock.
func (rm *RunManager) FinishIfNeeded(ctx context.Context, block BlockInfo) error {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return fmt.Errorf("run not found. task_id = %d", rm.runId)
	}
	epoch := rs.Epoch.LastEpoch

	active, err := rm.GetEpochActiveNodes(ctx, epoch, block)
	if err != nil {
		return err
	}
	joined := len(active)
	enough := joined == rm.maxNodes
	enoughTimeout := joined >= rm.minNodes && block.height-rs.Epoch.LastEpochBlockHeight > rm.joinTimeout

	if !(enough || enoughTimeout) {
		return nil
	}
	// reuse AssignRank (which also marks FinishedEpochs)
	return rm.AssignRank(ctx, block)
}

func (rm *RunManager) SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier) {
	rm.store.SetBarrier(ctx, barrier)
}

func (rm *RunManager) GetBarrierStatus(ctx context.Context, req *types.GetBarrierStatusRequest) (*types.GetBarrierStatusResponse, error) {
	task := rm.store.GetRunState(ctx, rm.runId)
	if task == nil {
		return nil, fmt.Errorf("run not found. task_id = %d", rm.runId)
	}

	if req.Epoch > task.Epoch.LastEpoch {
		return &types.GetBarrierStatusResponse{
			AllReady:   false,
			NotReady:   nil,
			AliveNodes: nil,
		}, nil
	}

	aliveNodes, err := rm.GetEpochActiveNodes(ctx, req.Epoch, NewBlockInfo(sdk.UnwrapSDKContext(ctx)))
	if err != nil {
		return nil, err
	}

	key := types.TrainingTaskBarrierEpochKey{
		BarrierId: req.BarrierId,
		TaskId:    rm.runId,
		Epoch:     req.Epoch,
	}
	barriers, err := rm.store.GetBarrierEpochStatus(ctx, key)
	if err != nil {
		return nil, err
	}

	// Check which alive nodes have a barrier entry
	barrierMap := make(map[NodeId]bool)
	for _, barrier := range barriers {
		nodeId := NodeId{
			Participant: barrier.Participant,
			NodeId:      barrier.NodeId,
		}
		barrierMap[nodeId] = true
	}

	aliveIds := make([]string, 0)
	notReady := make([]string, 0)
	for _, node := range aliveNodes {
		nodeIdString := fmt.Sprintf("%s/%s", node.Participant, node.NodeId)
		aliveIds = append(aliveIds, nodeIdString)

		if _, ok := barrierMap[node]; !ok {
			notReady = append(notReady, nodeIdString)
		}
	}

	return &types.GetBarrierStatusResponse{
		AllReady:   len(notReady) == 0,
		NotReady:   notReady,
		AliveNodes: aliveIds,
	}, nil
}

/*
func (rm *RunManager) rerankIfSomeNodesLeft(ctx context.Context, epoch int32, block BlockInfo) error {
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
	var original []NodeId
	for id, rec := range es.Activity {
		if rec.Rank != -1 {
			original = append(original, id)
		}
	}
	sortNodeIds(original)

	// collect still‑alive
	aliveMap := map[string]bool{}
	active, err := rm.GetEpochActiveNodes(ctx, epoch, block)
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
*/

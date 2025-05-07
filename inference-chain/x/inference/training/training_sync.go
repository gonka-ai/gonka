package training

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// EpochState holds per‑epoch membership info.
type OuterStepState struct {
	OuterStep int32
	Activity  map[GlobalNodeId]*types.TrainingTaskNodeEpochActivity
}

func NewOuterStepState(activity []*types.TrainingTaskNodeEpochActivity) (*OuterStepState, error) {
	if len(activity) == 0 {
		return nil, fmt.Errorf("empty activity")
	}

	outerStep := activity[0].Heartbeat.OuterStep
	activityMap := make(map[GlobalNodeId]*types.TrainingTaskNodeEpochActivity, len(activity))
	for _, rec := range activity {
		if outerStep != rec.Heartbeat.OuterStep {
			return nil, fmt.Errorf("OuterStep doesn't matach. OuterStep = %d, rec.Hearbeat.OuterStep = %d. rec.NodeId = %s", outerStep, rec.Heartbeat.OuterStep, rec.NodeId)
		}
		key := GlobalNodeId{
			Participant: rec.Participant,
			LocalNodeId: rec.NodeId,
		}
		activityMap[key] = rec
	}

	return &OuterStepState{
		OuterStep: activity[0].Heartbeat.OuterStep,
		Activity:  activityMap,
	}, nil
}

func (os *OuterStepState) filterActive(currentBlock BlockInfo, heartbeatTimeout int64) OuterStepState {
	active := make(map[GlobalNodeId]*types.TrainingTaskNodeEpochActivity)

	for nodeId, rec := range os.Activity {
		blockDiff := currentBlock.height - rec.Heartbeat.BlockHeight
		if blockDiff <= heartbeatTimeout {
			active[nodeId] = rec
		}
	}

	return OuterStepState{
		OuterStep: os.OuterStep,
		Activity:  active,
	}
}

func (os *OuterStepState) getSortedNodeIds() []GlobalNodeId {
	nodeIds := make([]GlobalNodeId, 0, len(os.Activity))
	for nodeId := range os.Activity {
		nodeIds = append(nodeIds, nodeId)
	}
	sortNodeIds(nodeIds)
	return nodeIds
}

func (os *OuterStepState) toActivityArray() []*types.TrainingTaskNodeEpochActivity {
	activity := make([]*types.TrainingTaskNodeEpochActivity, 0, len(os.Activity))
	for _, rec := range os.Activity {
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

	GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, nodeId GlobalNodeId) *types.TrainingTaskNodeEpochActivity
	SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity)

	SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier)
	GetBarrierEpochStatus(ctx context.Context, key types.TrainingTaskBarrierEpochKey) ([]*types.TrainingTaskBarrier, error)
}

// RunMembershipService is the public API.
type RunMembershipService interface {
	Join(ctx context.Context, nodeId GlobalNodeId, epoch int32, block BlockInfo) error
	JoinStatus(ctx context.Context, nodeId GlobalNodeId, epoch int32, block BlockInfo) (*types.MLNodeTrainStatus, error)
	Heartbeat(ctx context.Context, nodeId GlobalNodeId, req *types.HeartbeatRequest, block BlockInfo) error
	GetEpochActiveNodes(ctx context.Context, epoch int32, block BlockInfo) ([]GlobalNodeId, error)
	AssignRank(ctx context.Context, block BlockInfo) error
	FinishIfNeeded(ctx context.Context, block BlockInfo) (bool, error)
	RerankIfSomeNodesLeft(ctx context.Context, epoch int32, block BlockInfo) error
	SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier, block BlockInfo) error
	GetBarrierStatus(ctx context.Context, req *types.GetBarrierStatusRequest) (*types.GetBarrierStatusResponse, error)
}

type GlobalNodeId struct {
	Participant string
	LocalNodeId string
}

func NewGlobalNodeId(globalNodeId string) (*GlobalNodeId, error) {
	// Check globalNodeId contains only one slash /
	if len(globalNodeId) == 0 {
		return nil, fmt.Errorf("empty globalNodeId")
	}
	if globalNodeId[0] == '/' {
		return nil, fmt.Errorf("globalNodeId should not start with /")
	}
	if globalNodeId[len(globalNodeId)-1] == '/' {
		return nil, fmt.Errorf("globalNodeId should not end with /")
	}
	if !strings.Contains(globalNodeId, "/") {
		return nil, fmt.Errorf("globalNodeId should contain /")
	}
	splitRes := strings.Split(globalNodeId, "/")
	if len(splitRes) != 2 {
		return nil, fmt.Errorf("globalNodeId should contain only one /")
	}
	if len(splitRes[0]) == 0 || len(splitRes[1]) == 0 {
		return nil, fmt.Errorf("globalNodeId should not contain empty strings")
	}
	address := splitRes[0]
	localNodeId := splitRes[1]
	return &GlobalNodeId{Participant: address, LocalNodeId: localNodeId}, nil
}

type LocalNodeId struct {
	id string
}

func NewLocalNodeId(globalNodeId string) (*LocalNodeId, error) {
	global, err := NewGlobalNodeId(strings.TrimSpace(globalNodeId))
	if err != nil {
		return nil, err
	}
	return &LocalNodeId{id: global.LocalNodeId}, nil
}

func (n *GlobalNodeId) ToString() string {
	return fmt.Sprintf("%s/%s", n.Participant, n.LocalNodeId)
}

type RunManager struct {
	runId            uint64
	store            RunStore
	joinTimeout      int64
	heartbeatTimeout int64
	logger           types.InferenceLogger
}

// FIXME: should we use blocks or time?
const (
	defaultJoinTimeout      = 30 // 30 blocks
	defaultHeartbeatTimeout = 30 // 30 blocks
)

func NewRunManager(
	runId uint64,
	store RunStore,
	logger types.InferenceLogger,
) *RunManager {
	return &RunManager{
		runId:            runId,
		store:            store,
		joinTimeout:      defaultJoinTimeout,
		heartbeatTimeout: defaultHeartbeatTimeout,
		logger:           logger,
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
func sortNodeIds(nodeIds []GlobalNodeId) {
	sort.Slice(nodeIds, func(i, j int) bool {
		if nodeIds[i].Participant != nodeIds[j].Participant {
			return nodeIds[i].Participant < nodeIds[j].Participant
		}
		return nodeIds[i].LocalNodeId < nodeIds[j].LocalNodeId
	})
}

func (rm *RunManager) Join(ctx sdk.Context, nodeId GlobalNodeId, outerStep int32, block BlockInfo) error {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return fmt.Errorf("run not found. task_id = %d", rm.runId)
	}

	if rs.Epoch == nil {
		rm.logger.LogError("RunManager.Join: rs.Epoch is unexpectedly nil, setting to empty epoch", types.Training, "runId", rm.runId)
		rs.Epoch = NewEmptyEpochInfo()
	}

	lastEpoch := rs.Epoch.LastEpoch
	if outerStep < -1 {
		return fmt.Errorf("bad request. invalid outer step %d", outerStep)
	}
	if outerStep < lastEpoch {
		return fmt.Errorf("joining outdated outer step %d, last is %d", outerStep, lastEpoch)
	}
	if outerStep == lastEpoch && rs.Epoch.LastEpochIsFinished {
		return fmt.Errorf("joining outer step %d after finish", outerStep)
	}

	// new epoch
	if outerStep > lastEpoch {
		rs.Epoch.LastEpoch = outerStep
		rs.Epoch.LastEpochIsFinished = false
		rs.Epoch.LastEpochBlockHeight = block.height
		rs.Epoch.LastEpochTimestamp = block.timestamp.Unix()

		if err := rm.store.SaveRunState(ctx, rs); err != nil {
			return err
		}
	}

	progress := taskProgress{
		InnerStep: 0,
		OuterStep: outerStep,
		Epoch:     0,
	}

	activity := rm.getOrCreateActivityEntry(ctx, nodeId, progress)
	updateHeartbeat(&activity, block)
	rm.store.SaveParticipantActivity(ctx, &activity)

	_, err := rm.FinishIfNeeded(ctx, block)
	return err
}

func updateHeartbeat(a *types.TrainingTaskNodeEpochActivity, block BlockInfo) {
	a.Heartbeat.BlockHeight = block.height
	a.Heartbeat.BlockTime = block.timestamp.Unix()
}

type taskProgress struct {
	InnerStep int32
	OuterStep int32
	Epoch     int32
}

func (rm *RunManager) getOrCreateActivityEntry(ctx context.Context, nodeId GlobalNodeId, taskProgress taskProgress) types.TrainingTaskNodeEpochActivity {
	activity := rm.store.GetParticipantActivity(ctx, rm.runId, taskProgress.OuterStep, nodeId)
	if activity == nil {
		activity = &types.TrainingTaskNodeEpochActivity{
			TaskId:      rm.runId,
			Participant: nodeId.Participant,
			NodeId:      nodeId.LocalNodeId,
			Heartbeat: &types.TrainingTaskHeartbeat{
				InnerStep:   taskProgress.InnerStep,
				OuterStep:   taskProgress.OuterStep,
				Epoch:       taskProgress.Epoch,
				BlockHeight: 0,
				BlockTime:   0,
			},
			Rank: -1, // TODO: are we sure -1?
		}
	}
	return *activity
}

func (rm *RunManager) JoinStatus(ctx context.Context, nodeId GlobalNodeId, outerStep int32, block BlockInfo) (*types.MLNodeTrainStatus, error) {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return &types.MLNodeTrainStatus{
			Status:      types.MLNodeTrainStatusEnum_ERROR,
			NodeId:      nodeId.ToString(),
			OuterStep:   outerStep,
			ActiveNodes: make([]string, 0),
			Rank:        -1,
		}, nil
	}

	activity := rm.store.GetParticipantActivity(ctx, rm.runId, outerStep, nodeId)
	if activity != nil {
		updateHeartbeat(activity, block)
		rm.store.SaveParticipantActivity(ctx, activity)
	}

	finished, err := rm.FinishIfNeeded(ctx, block)
	if err != nil {
		return nil, err
	}

	if finished {
		err = rm.rerankIfSomeNodesLeft(ctx, outerStep, block)
		if err != nil {
			return nil, err
		}
	}

	aliveNodes, err := rm.GetEpochActiveNodes(ctx, outerStep, block)
	if err != nil {
		return nil, err
	}
	aliveNodeIds := make([]string, len(aliveNodes))
	for i, n := range aliveNodes {
		aliveNodeIds[i] = n.ToString()
	}

	activity = rm.store.GetParticipantActivity(ctx, rm.runId, outerStep, nodeId)
	if activity == nil || activity.Rank == -1 {
		return &types.MLNodeTrainStatus{
			Status:      types.MLNodeTrainStatusEnum_NOT_JOINED,
			NodeId:      nodeId.ToString(),
			OuterStep:   outerStep,
			ActiveNodes: aliveNodeIds,
			Rank:        -1,
		}, nil
	} else {
		return &types.MLNodeTrainStatus{
			Status:      types.MLNodeTrainStatusEnum_OK,
			NodeId:      nodeId.ToString(),
			OuterStep:   outerStep,
			ActiveNodes: aliveNodeIds,
			Rank:        activity.Rank,
		}, nil
	}
}

func (rm *RunManager) Heartbeat(ctx sdk.Context, nodeId GlobalNodeId, req *types.HeartbeatRequest, block BlockInfo) error {
	progress := taskProgress{
		InnerStep: req.InnerStep,
		OuterStep: req.OuterStep,
		Epoch:     req.Epoch,
	}

	activity := rm.getOrCreateActivityEntry(ctx, nodeId, progress)
	updateHeartbeat(&activity, block)
	rm.store.SaveParticipantActivity(ctx, &activity)

	_, err := rm.FinishIfNeeded(ctx, block)
	return err
}

func (rm *RunManager) GetEpochActiveNodes(ctx context.Context, epoch int32, currentBlock BlockInfo) ([]GlobalNodeId, error) {
	es, err := rm.getEpochStateActiveFiltered(ctx, epoch, currentBlock)
	if err != nil {
		return nil, err
	}
	return es.getSortedNodeIds(), nil
}

func (rm *RunManager) getEpochStateActiveFiltered(ctx context.Context, epoch int32, currentBlock BlockInfo) (*OuterStepState, error) {
	activity, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return nil, err
	}
	es, err := NewOuterStepState(activity)
	if err != nil {
		return nil, err
	}

	filteredEs := es.filterActive(currentBlock, rm.heartbeatTimeout)
	return &filteredEs, nil
}

func (rm *RunManager) AssignRank(ctx context.Context, block BlockInfo) error {
	rm.logger.LogInfo("RunManager.AssignRank", types.Training, "runId", rm.runId, "blockHeight", block.height)
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return fmt.Errorf("run not found. task_id = %d", rm.runId)
	}
	epoch := rs.Epoch.LastEpoch

	epochState, err := rm.getEpochStateActiveFiltered(ctx, epoch, block)
	if err != nil {
		return err
	}
	active := epochState.Activity
	nodeNumParams := getNodeNumParams(rs)

	if len(active) < nodeNumParams.minNodes || len(active) > nodeNumParams.maxNodes {
		rm.logger.LogInfo("RunManager.AssignRank. cannot assign ranks", types.Training, "runId", rm.runId, "len(active)", len(active), "minNodes", nodeNumParams.minNodes, "maxNodes", nodeNumParams.maxNodes)
		return fmt.Errorf("cannot assign rank: active=%d, want [%d,%d]",
			len(active), nodeNumParams.minNodes, nodeNumParams.maxNodes)
	}

	rm.logger.LogInfo("Proceeding to assign ranks and mark step as finished", types.Training, "runId", rm.runId, "step", rs.Epoch.LastEpoch)
	nodeIds := epochState.getSortedNodeIds()
	for i, nodeId := range nodeIds {
		epochState.Activity[nodeId].Rank = int32(i)
	}

	if err := rm.store.SaveEpochState(ctx, epochState.toActivityArray()); err != nil {
		return err
	}

	rs.Epoch.LastEpochIsFinished = true
	return rm.store.SaveRunState(ctx, rs)
}

// FinishIfNeeded is the exported version of finishIfNeededNoLock.
func (rm *RunManager) FinishIfNeeded(ctx context.Context, block BlockInfo) (bool, error) {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return false, fmt.Errorf("run not found. task_id = %d", rm.runId)
	}
	epoch := rs.Epoch.LastEpoch

	active, err := rm.GetEpochActiveNodes(ctx, epoch, block)
	if err != nil {
		return false, err
	}
	joined := len(active)
	nodeNumParams := getNodeNumParams(rs)
	enough := joined == nodeNumParams.maxNodes
	enoughTimeout := joined >= nodeNumParams.minNodes && block.height-rs.Epoch.LastEpochBlockHeight > rm.joinTimeout

	rm.logger.LogInfo("RunManager.FinishIfNeeded", types.Training, "enough", enough, "enoughTimeout", enoughTimeout)
	if !(enough || enoughTimeout) {
		return false, nil
	}

	err = rm.AssignRank(ctx, block)
	if err != nil {
		return false, err
	} else {
		return true, nil
	}
}

type minAndMaxNodesParams struct {
	maxNodes int
	minNodes int
}

func getNodeNumParams(task *types.TrainingTask) minAndMaxNodesParams {
	maxNodes := 0
	for _, a := range task.Assignees {
		maxNodes = maxNodes + len(a.NodeIds)
	}
	return minAndMaxNodesParams{
		maxNodes: maxNodes,
		minNodes: max(maxNodes-1, 0),
	}
}

func (rm *RunManager) SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier) {
	rm.store.SetBarrier(ctx, barrier)
}

func (rm *RunManager) GetBarrierStatus(ctx context.Context, req *types.GetBarrierStatusRequest) (*types.GetBarrierStatusResponse, error) {
	task := rm.store.GetRunState(ctx, rm.runId)
	if task == nil {
		return nil, fmt.Errorf("run not found. task_id = %d", rm.runId)
	}

	if req.OuterStep > task.Epoch.LastEpoch {
		return &types.GetBarrierStatusResponse{
			AllReady:   false,
			NotReady:   nil,
			AliveNodes: nil,
		}, nil
	}

	aliveNodes, err := rm.GetEpochActiveNodes(ctx, req.OuterStep, NewBlockInfo(sdk.UnwrapSDKContext(ctx)))
	if err != nil {
		return nil, err
	}

	key := types.TrainingTaskBarrierEpochKey{
		BarrierId: req.BarrierId,
		TaskId:    rm.runId,
		OuterStep: req.OuterStep,
	}
	barriers, err := rm.store.GetBarrierEpochStatus(ctx, key)
	if err != nil {
		return nil, err
	}

	// Check which live nodes have a barrier entry
	barrierMap := make(map[GlobalNodeId]bool)
	for _, barrier := range barriers {
		nodeId := GlobalNodeId{
			Participant: barrier.Participant,
			LocalNodeId: barrier.NodeId,
		}
		barrierMap[nodeId] = true
	}

	aliveIds := make([]string, 0)
	notReady := make([]string, 0)
	for _, nodeId := range aliveNodes {
		nodeIdString := nodeId.ToString()
		aliveIds = append(aliveIds, nodeIdString)

		if _, ok := barrierMap[nodeId]; !ok {
			notReady = append(notReady, nodeIdString)
		}
	}

	return &types.GetBarrierStatusResponse{
		AllReady:   len(notReady) == 0,
		NotReady:   notReady,
		AliveNodes: aliveIds,
	}, nil
}

func (rm *RunManager) rerankIfSomeNodesLeft(ctx context.Context, epoch int32, block BlockInfo) error {
	rs := rm.store.GetRunState(ctx, rm.runId)
	if rs == nil {
		return fmt.Errorf("run not found. task_id = %d", rm.runId)
	}

	if epoch == rs.Epoch.LastEpoch && !rs.Epoch.LastEpochIsFinished {
		return fmt.Errorf("epoch %d not finished", epoch)
	} else if epoch > rs.Epoch.LastEpoch {
		return fmt.Errorf("Unexpected epoch received in rerank not finished. epoch = %d. lastEpoch = %d", epoch, rs.Epoch.LastEpoch)
	} else if epoch < rs.Epoch.LastEpoch {
		// TODO: log epoch
	}

	activity, err := rm.store.GetEpochState(ctx, rm.runId, epoch)
	if err != nil {
		return err
	}
	es, err := NewOuterStepState(activity)
	if err != nil {
		return err
	}

	var original []GlobalNodeId
	for nodeId, rec := range es.Activity {
		if rec.Rank != -1 {
			original = append(original, nodeId)
		}
	}
	sortNodeIds(original)

	activeEs := es.filterActive(block, rm.heartbeatTimeout)

	// if some dropped, reassign among survivors
	var survivors []GlobalNodeId
	for _, nodeId := range original {
		if _, ok := activeEs.Activity[nodeId]; ok {
			survivors = append(survivors, nodeId)
		}
	}

	if len(survivors) < len(original) {
		rm.logger.LogInfo("RunManager.rerankIfSomeNodesLeft len(survivors) < len(original), reranking", types.Training, "runId", rm.runId, "epoch", epoch, "original", original, "survivors", survivors)
		for rank, nodeID := range survivors {
			es.Activity[nodeID].Rank = int32(rank)
		}
		for _, nodeID := range original {
			if _, ok := activeEs.Activity[nodeID]; !ok {
				es.Activity[nodeID].Rank = -1
			}
		}
		return rm.store.SaveEpochState(ctx, activeEs.toActivityArray())
	}

	return nil
}

func NewEmptyEpochInfo() *types.EpochInfo {
	return &types.EpochInfo{
		LastEpoch:            -1,
		LastEpochIsFinished:  false,
		LastEpochBlockHeight: 0,
		LastEpochTimestamp:   0,
	}
}

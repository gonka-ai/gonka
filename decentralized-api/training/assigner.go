package training

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"fmt"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

type Assigner struct {
	cosmosClient     cosmosclient.CosmosMessageClient
	tendermintClient *cosmosclient.TendermintClient
	ctx              context.Context
	task             *taskToAssignState
}

type taskToAssignState struct {
	task *types.TrainingTask
}

const logTag = "[training-task-assigner] "

func NewAssigner(client cosmosclient.CosmosMessageClient, tendermintClient *cosmosclient.TendermintClient, ctx context.Context) *Assigner {
	assigner := &Assigner{
		cosmosClient:     client,
		tendermintClient: tendermintClient,
		ctx:              ctx,
		task:             nil,
	}

	// TODO: on startup do some queries to restore state (like tasks I was assigned)
	go assigner.claimTasksForAssignment()

	return assigner
}

func (a *Assigner) claimTasksForAssignment() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if a.task == nil {
				a.tryClaimingTaskToAssign()
			} else {
				a.checkTaskIsStillClaimed()
			}

			// Task could be assigned in the "if" above, thus we're rechecking here
			if a.task != nil {
				a.assignTask(*a.task.task)
			}
		}
	}
}

func (a *Assigner) tryClaimingTaskToAssign() {
	chainStatus, err := a.tendermintClient.Status()
	if err != nil {
		slog.Error(logTag+"Failed to query chain status", "err", err)
		return
	}
	if chainStatus == nil {
		slog.Error(logTag + "Failed to query chain status: empty response")
		return
	}

	if chainStatus.SyncInfo.CatchingUp {
		slog.Info(logTag + "Node is catching up, skipping task query")
		return
	}

	blockHeight := chainStatus.SyncInfo.LatestBlockHeight
	queryClient := a.cosmosClient.NewInferenceQueryClient()

	req := &types.QueryQueuedTrainingTasksRequest{}
	resp, err := queryClient.QueuedTrainingTasks(a.cosmosClient.GetContext(), req)
	if err != nil {
		slog.Error(logTag+"Error querying for training tasks", "err", err)
		return
	}

	task := a.chooseTrainingTask(resp.Tasks, blockHeight)
	if task == nil {
		// slog.Info(logTag + "No training tasks to claim for assignment")
		return
	}

	msg := inference.MsgClaimTrainingTaskForAssignment{
		TaskId: task.Id,
	}

	_, err = a.cosmosClient.ClaimTrainingTaskForAssignment(&msg)
	if err != nil {
		slog.Error(logTag+"Error claiming task for assignment", "err", err)
		return
	}

	slog.Info(logTag+"Claimed task for assignment", "taskId", task.Id)
	a.task = &taskToAssignState{
		task: task,
	}
}

func (a *Assigner) findAlreadyClaimedTask(tasks []*types.TrainingTask) *types.TrainingTask {
	for _, task := range tasks {
		if task.Assigner == a.cosmosClient.GetAccountAddress() {
			return task
		}
	}
	return nil
}

func (a *Assigner) chooseTrainingTask(tasks []*types.TrainingTask, currentBlockHeight int64) *types.TrainingTask {
	// This check handles the case of the network node being restarted while the task was already claimed by it
	taskAlreadyAssignedToMe := a.findAlreadyClaimedTask(tasks)
	if taskAlreadyAssignedToMe != nil {
		slog.Info(logTag+"Already claimed task found", "taskId", taskAlreadyAssignedToMe.Id)
		return taskAlreadyAssignedToMe
	}

	unclaimedTasks := make([]*types.TrainingTask, 0)
	for _, task := range tasks {
		if task.AssignedAtBlockHeight == 0 && (task.Assigner == "" || (uint64(currentBlockHeight)-task.ClaimedByAssignerAtBlockHeight) > keeper.TrainingTaskAssignmentDeadline) {
			unclaimedTasks = append(unclaimedTasks, task)
		}
	}

	if len(unclaimedTasks) == 0 {
		return nil
	}

	i := rand.Intn(len(unclaimedTasks))
	return unclaimedTasks[i]
}

func (a *Assigner) assignTask(task types.TrainingTask) {
	queryClient := a.cosmosClient.NewInferenceQueryClient()
	participants, err := getParticipantsWithHardwareNodes(a.ctx, queryClient)
	if err != nil {
		slog.Error(logTag+"Error querying participants with hardware nodes", "err", err)
		return
	}

	selectedParticipants, err := getParticipantListMatchingHardwareSpec(task.HardwareResources, participants)
	if err != nil {
		// FIXME: Returning and sleeping 60 more secs. Not sure if it's the best strategy
		//  We need to be able to distinguish between:
		//   a. "can't do because everyone's busy"
		//   vs
		//   b. "can't do because network doesn't have required hardware"
		//  And the treat them differently
		//   a. Retry, but when?
		//   b. Mark task as failed?
		slog.Error(logTag+"Error picking task", "err", err)
		return
	}
	slog.Info(logTag+"Selected participants", "participants", selectedParticipants)

	httpClient := utils.NewHttpClient(120 * time.Second)
	for _, p := range selectedParticipants {
		participant, err := queryClient.Participant(a.ctx, &types.QueryGetParticipantRequest{Index: p.participant})
		if err != nil {
			slog.Error(logTag+"Error querying for participant", "participant", p.participant, "err", err)
			return
		}

		err = confirmAvailability(httpClient, participant.Participant.InferenceUrl, task.Id, p.nodeIds)
		if err != nil {
			// FIXME: Returning and sleeping 60 more secs.
			// 	Because by the next iteration chain state of hardware nodes may become up to date
			//   and we would select a different set of participants
			slog.Error(logTag+"Error confirming availability", "participant", p.participant, "err", err)
			return
		}
	}
	slog.Info(logTag+"Confirmed availability for participants", "participants", selectedParticipants)

	assignees := make([]*inference.TrainingTaskAssignee, len(selectedParticipants))
	for i, p := range selectedParticipants {
		assignees[i] = &inference.TrainingTaskAssignee{
			Participant: p.participant,
			NodeIds:     p.nodeIds,
		}
	}
	msg := &inference.MsgAssignTrainingTask{
		TaskId:    task.Id,
		Assignees: assignees,
	}
	_, err = a.cosmosClient.AssignTrainingTask(msg)
	if err != nil {
		slog.Error(logTag+"Error sending assign task transaction", "err", err)
		// TODO: what should we do? We need to know the reason, maybe someone else assigned it
		//  Get back here once you implement msg processing and understand what can go wrong here
	} else {
		slog.Info(logTag+"Assigned task", "taskId", task.Id)
		a.task = nil
	}
}

func (a *Assigner) checkTaskIsStillClaimed() {
	if a.task == nil {
		return
	}

	queryClient := a.cosmosClient.NewInferenceQueryClient()
	req := &types.QueryTrainingTaskRequest{
		Id: a.task.task.Id,
	}
	resp, err := queryClient.TrainingTask(a.cosmosClient.GetContext(), req)
	if err != nil {
		slog.Error(logTag+"Error querying for task", "taskId", a.task.task.Id, "err", err)
		return
	}

	if resp.Task.Assigner != a.cosmosClient.GetAccountAddress() {
		slog.Info(logTag+"Task is no longer claimed by me", "taskId", a.task.task.Id)
		a.task = nil
	}

	if resp.Task.AssignedAtBlockHeight > 0 {
		slog.Info(logTag+"Task is already assigned", "taskId", a.task.task.Id)
		a.task = nil
	}
}

type participantHardwareNodes struct {
	participant string
	weight      int64
	hardware    *types.HardwareNodes
}

func getParticipantsWithHardwareNodes(ctx context.Context, queryClient types.QueryClient) (map[string]participantHardwareNodes, error) {
	req := &types.QueryCurrentEpochGroupDataRequest{}
	resp, err := queryClient.CurrentEpochGroupData(ctx, req)
	if err != nil {
		slog.Error(logTag+"Error querying for current epoch group data", "err", err)
		return nil, err
	}

	participants := resp.EpochGroupData.ValidationWeights
	slog.Info(logTag+"Participants", "participants", participants)

	// FIXME: could be optimized if we queried only nodeIds of actual participants instead of ALL participants
	//  or maybe we should do some hardware nodeIds pruning
	r := &types.QueryHardwareNodesAllRequest{}
	hardwareNodes, err := queryClient.HardwareNodesAll(ctx, r)
	if err != nil {
		slog.Error(logTag+"Error querying for hardware nodeIds", "err", err)
		return nil, err
	}

	hardwareNodesByParticipant := make(map[string]*types.HardwareNodes)
	for _, nodes := range hardwareNodes.Nodes {
		hardwareNodesByParticipant[nodes.Participant] = nodes
	}

	participantsWithHardware := make(map[string]participantHardwareNodes)
	for _, participant := range participants {
		address := participant.MemberAddress
		participantsWithHardware[address] = participantHardwareNodes{
			participant: address,
			weight:      participant.Weight,
			hardware:    hardwareNodesByParticipant[address],
		}
	}
	slog.Info(logTag+"Participants with hardware nodes", "participants", participantsWithHardware)

	return participantsWithHardware, nil
}

type selectedParticipant struct {
	participant string
	nodeIds     []string
}

type candidateNode struct {
	participant       string
	participantWeight int64
	nodeId            string
	available         map[string]uint32
}

// getParticipantListMatchingHardwareSpec returns a mapping from participant IDs to the list of node IDs
// that, when combined, cover the task's hardware requirements. Returns an error if no such set exists.
func getParticipantListMatchingHardwareSpec(
	hardwareRequirements []*types.TrainingHardwareResources,
	participants map[string]participantHardwareNodes,
) ([]selectedParticipant, error) {
	remaining := make(map[string]uint32)
	for _, req := range hardwareRequirements {
		remaining[req.Type] += req.Count
	}
	slog.Info(logTag+"Matching hardware requirements", "requirements", hardwareRequirements)

	// Flatten the candidateNode pool: one candidateNode per available node.
	var candidates []candidateNode
	for _, p := range participants {
		if p.hardware == nil {
			continue
		}
		for _, node := range p.hardware.HardwareNodes {
			if node.Status != types.HardwareNodeStatus_INFERENCE {
				continue
			}
			avail := make(map[string]uint32)
			for _, hw := range node.Hardware {
				avail[hw.Type] += hw.Count
			}
			candidates = append(candidates, candidateNode{
				participant:       p.participant,
				participantWeight: p.weight,
				nodeId:            node.LocalId,
				available:         avail,
			})
		}
	}

	// Sort candidates by participantWeight descending (higher participantWeight first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].participantWeight > candidates[j].participantWeight
	})
	slog.Info(logTag+"Candidates", "candidates", candidates)

	// We'll mark which candidates have been selected.
	selected := make([]bool, len(candidates))

	var selectedCandidates []candidateNode

	// Greedy loop: try to cover the remaining requirements.
	for {
		allRequirementsMet := true
		for _, req := range remaining {
			if req > 0 {
				allRequirementsMet = false
				break
			}
		}
		if allRequirementsMet {
			break
		}

		bestCandidateIdx := findHighestContributingCandidate(candidates, selected, remaining)
		if bestCandidateIdx == -1 {
			return nil, fmt.Errorf(logTag + "insufficient hardware across nodeIds to satisfy task requirements")
		}

		// Select the best candidateNode and update the remaining requirements.
		selected[bestCandidateIdx] = true
		selectedCandidates = append(selectedCandidates, candidates[bestCandidateIdx])
		bestCandidate := candidates[bestCandidateIdx]
		for hwType, availCount := range bestCandidate.available {
			if need, ok := remaining[hwType]; ok && need > 0 {
				if availCount >= need {
					remaining[hwType] = 0
				} else {
					remaining[hwType] = need - availCount
				}
			}
		}
	}

	resultMap := make(map[string][]string)
	for _, cand := range selectedCandidates {
		resultMap[cand.participant] = append(resultMap[cand.participant], cand.nodeId)
	}

	result := make([]selectedParticipant, 0, len(resultMap))
	for participant, nodes := range resultMap {
		result = append(result, selectedParticipant{
			participant: participant,
			nodeIds:     nodes,
		})
	}

	return result, nil
}

func findHighestContributingCandidate(candidates []candidateNode, selected []bool, remaining map[string]uint32) int {
	var bestCandidateIdx int = -1
	var bestContribution uint32 = 0

	for i, cand := range candidates {
		if selected[i] {
			continue
		}
		var contribution uint32 = 0
		for hwType, availCount := range cand.available {
			if need, ok := remaining[hwType]; ok && need > 0 {
				if availCount < need {
					contribution += availCount
				} else {
					contribution += need
				}
			}
		}
		// Update the best candidateNode if this one offers a higher contribution.
		if contribution > bestContribution {
			bestContribution = contribution
			bestCandidateIdx = i
		}
	}

	return bestCandidateIdx
}

type lockTrainingNodesDto struct {
	TrainingTaskId uint64   `json:"training_task_id"`
	NodeIds        []string `json:"node_ids"`
}

func confirmAvailability(client *http.Client, participantUrl string, taskId uint64, nodeIds []string) error {
	logging.Debug("confirmAvailability", types.Training, "participantUrl", participantUrl, "taskId", taskId, "nodeIds", nodeIds)
	url := participantUrl + "/v1/training/lock-nodes"
	payload := lockTrainingNodesDto{
		TrainingTaskId: taskId,
		NodeIds:        nodeIds,
	}
	_, err := utils.SendPostJsonRequest(context.Background(), client, url, payload)
	return err
}
